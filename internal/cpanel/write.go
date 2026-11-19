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
	resp, err := c.http.Do(req)
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
	// Some API2 functions report per-item failures inside data: [{"result":0,"reason":"..."}].
	var items []struct {
		Result *int   `json:"result"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(r.Result.Data, &items) == nil {
		for _, it := range items {
			if it.Result != nil && *it.Result == 0 {
				return nil, &APIError{Module: module, Function: function, Errors: []string{it.Reason}}
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
	hc := *c.http
	hc.Timeout = 0 // large file; the run context bounds it
	resp, err := hc.Do(req)
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
