package cpanel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// API2 calls a legacy cPanel API 2 function (needed where UAPI has no equivalent, such as
// SubDomain::delsubdomain and Fileman::mkdir / fileop).
func (c *Client) API2(ctx context.Context, module, function string, args url.Values) (json.RawMessage, error) {
	return c.api2(ctx, c.http, module, function, args)
}

// longHTTP is the client without its per-request timeout, for file operations that can take
// minutes (large uploads, extracting an archive). The caller's context bounds them.
func (c *Client) longHTTP() *http.Client {
	hc := *c.http
	hc.Timeout = 0
	return &hc
}

func (c *Client) api2(ctx context.Context, hc *http.Client, module, function string, args url.Values) (json.RawMessage, error) {
	prefix := ""
	if c.opt.Auth == AuthSession {
		if c.token == "" {
			return nil, errors.New("not logged in")
		}
		prefix = c.token
	}
	q := url.Values{}
	for k, v := range args {
		q[k] = v
	}
	q.Set("cpanel_jsonapi_user", c.opt.Username)
	q.Set("cpanel_jsonapi_apiversion", "2")
	q.Set("cpanel_jsonapi_module", module)
	q.Set("cpanel_jsonapi_func", function)
	req, _ := http.NewRequestWithContext(ctx, "POST", c.opt.BaseURL+prefix+"/json-api/cpanel", strings.NewReader(q.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.opt.Auth == AuthToken {
		req.Header.Set("Authorization", "cpanel "+c.opt.Username+":"+c.opt.Secret)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	var r struct {
		Result struct {
			Data  json.RawMessage `json:"data"`
			Error string          `json:"error"`
			Event struct {
				Result int `json:"result"`
			} `json:"event"`
		} `json:"cpanelresult"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s::%s (api2): unexpected response (HTTP %d)", module, function, resp.StatusCode)
	}
	if r.Result.Error != "" || r.Result.Event.Result != 1 {
		msg := r.Result.Error
		if msg == "" {
			msg = "failed"
		}
		return nil, &APIError{Module: module, Function: function, Errors: []string{msg}}
	}
	// Some API2 functions report per-item failures inside data: [{"result":0,"reason":"..."}]
	// (Fileman::fileop uses "err" instead of "reason").
	var items []struct {
		Result *int   `json:"result"`
		Reason string `json:"reason"`
		Err    string `json:"err"`
	}
	if json.Unmarshal(r.Result.Data, &items) == nil {
		for _, it := range items {
			if it.Result != nil && *it.Result == 0 {
				msg := it.Reason
				if msg == "" {
					msg = it.Err
				}
				if msg == "" {
					msg = "failed"
				}
				return nil, &APIError{Module: module, Function: function, Errors: []string{msg}}
			}
		}
	}
	return r.Result.Data, nil
}

// SaveFile writes a text file (creates or overwrites) via Fileman::save_file_content.
func (c *Client) SaveFile(ctx context.Context, dir, name, content string) error {
	_, err := c.Call(ctx, "Fileman", "save_file_content", url.Values{"dir": {dir}, "file": {name}, "content": {content}, "from_charset": {"UTF-8"}, "to_charset": {"UTF-8"}})
	return err
}

// Upload sends a (binary) file to dir via Fileman::upload_files.
func (c *Client) Upload(ctx context.Context, dir, name string, data io.Reader) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("dir", dir)
	_ = mw.WriteField("overwrite", "1")
	fw, _ := mw.CreateFormFile("file-1", name)
	if _, err := io.Copy(fw, data); err != nil {
		return err
	}
	_ = mw.Close()
	prefix := ""
	if c.opt.Auth == AuthSession {
		prefix = c.token
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", c.opt.BaseURL+prefix+"/execute/Fileman/upload_files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.opt.Auth == AuthToken {
		req.Header.Set("Authorization", "cpanel "+c.opt.Username+":"+c.opt.Secret)
	}
	resp, err := c.longHTTP().Do(req) // large file; the run context bounds it
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r Result
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("upload: unexpected response (HTTP %d)", resp.StatusCode)
	}
	if r.Status != 1 {
		return &APIError{Module: "Fileman", Function: "upload_files", Errors: r.Errors}
	}
	return nil
}

// Mkdir creates dir (one level) via Fileman::mkdir.
func (c *Client) Mkdir(ctx context.Context, dir string) error {
	_, err := c.API2(ctx, "Fileman", "mkdir", url.Values{"path": {path.Dir(dir)}, "name": {path.Base(dir)}, "permissions": {"0755"}})
	return err
}

// Remove permanently deletes files or directories (absolute paths) via Fileman::fileop unlink.
func (c *Client) Remove(ctx context.Context, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := c.API2(ctx, "Fileman", "fileop", url.Values{"op": {"unlink"}, "sourcefiles": {strings.Join(paths, ",")}, "doubledecode": {"0"}})
	return err
}

// Extract unpacks an archive (absolute path) into destDir with cPanel's own File Manager
// (API2 Fileman::fileop op=extract). It runs inside cPanel rather than in a web request, so the
// site's PHP time, memory and process limits do not apply. destDir is created when missing.
func (c *Client) Extract(ctx context.Context, archive, destDir string) error {
	_ = c.Mkdir(ctx, destDir) // fails harmlessly when it already exists
	_, err := c.api2(ctx, c.longHTTP(), "Fileman", "fileop", url.Values{"op": {"extract"}, "sourcefiles": {archive}, "destfiles": {destDir}, "doubledecode": {"0"}})
	return err
}

// Entry is one item of a folder listing.
type Entry struct {
	Name string
	Dir  bool
}

// List returns the files and folders directly inside dir, hidden ones included.
func (c *Client) List(ctx context.Context, dir string) ([]Entry, error) {
	data, err := c.Call(ctx, "Fileman", "list_files", url.Values{"dir": {dir}, "show_hidden": {"1"}})
	if err != nil {
		return nil, err
	}
	var files []struct {
		File string `json:"file"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &files); err != nil {
		return nil, fmt.Errorf("list %s: unexpected response", dir)
	}
	out := make([]Entry, 0, len(files))
	for _, f := range files {
		if f.File == "" || f.File == "." || f.File == ".." {
			continue
		}
		out = append(out, Entry{Name: f.File, Dir: f.Type == "dir"})
	}
	return out, nil
}

// Exists reports whether an absolute path exists, by listing its parent folder. A parent that
// cannot be listed because it is gone itself means the path is gone too.
func (c *Client) Exists(ctx context.Context, p string) (bool, error) {
	p = path.Clean(p)
	dir, name := path.Dir(p), path.Base(p)
	list, err := c.List(ctx, dir)
	if err != nil {
		if dir == p || dir == "/" {
			return false, err
		}
		if up, uerr := c.Exists(ctx, dir); uerr == nil && !up {
			return false, nil
		}
		return false, err
	}
	for _, e := range list {
		if e.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// RemainError lists paths that are still there after RemoveAll tried every way it knows.
type RemainError struct{ Paths []string }

func (e *RemainError) Error() string { return "still present: " + strings.Join(e.Paths, ", ") }

// RemoveAll deletes files and folders (with everything inside them) and checks that they are
// gone. Fileman::fileop unlink removes folders recursively on current cPanel, but not every
// version or host does, so leftovers are retried with UAPI Fileman::delete_file (recursive, newer
// cPanel) and finally by emptying each folder from the bottom up. It returns *RemainError when
// something is still there afterwards; paths whose state cannot be checked count as remaining.
func (c *Client) RemoveAll(ctx context.Context, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	_ = c.Remove(ctx, paths...)
	left := c.remaining(ctx, paths)
	if len(left) == 0 {
		return nil
	}
	for _, p := range left {
		_, _ = c.Call(ctx, "Fileman", "delete_file", url.Values{"path": {p}})
	}
	if left = c.remaining(ctx, left); len(left) == 0 {
		return nil
	}
	for _, p := range left {
		c.removeTree(ctx, p, 0)
	}
	if left = c.remaining(ctx, left); len(left) > 0 {
		return &RemainError{Paths: left}
	}
	return nil
}

func (c *Client) remaining(ctx context.Context, paths []string) []string {
	var left []string
	for _, p := range paths {
		if ok, err := c.Exists(ctx, p); ok || err != nil {
			left = append(left, p)
		}
	}
	return left
}

// removeTree empties a folder depth-first with plain unlinks, then removes it.
func (c *Client) removeTree(ctx context.Context, p string, depth int) {
	if depth > 64 || ctx.Err() != nil {
		return
	}
	list, err := c.List(ctx, p)
	if err == nil {
		var files []string
		for _, e := range list {
			child := path.Join(p, e.Name)
			if e.Dir {
				c.removeTree(ctx, child, depth+1)
			} else {
				files = append(files, child)
			}
		}
		for len(files) > 0 {
			n := min(len(files), 100)
			_ = c.Remove(ctx, files[:n]...)
			files = files[n:]
		}
	}
	_ = c.Remove(ctx, p)
}
