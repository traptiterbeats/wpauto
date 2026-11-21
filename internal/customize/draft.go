package customize

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Brief is what the client tells about their business.
type Brief struct {
	Story          string            `json:"story,omitempty"` // free description of the client and their wishes
	SiteName       string            `json:"site_name"`
	BrandLatin     string            `json:"brand_latin,omitempty"`
	Business       string            `json:"business"` // field of work
	About          string            `json:"about"`    // who they are, what makes them different
	Services       string            `json:"services"` // services / products, one per line
	Audience       string            `json:"audience"`
	Tone           string            `json:"tone"`
	Email          string            `json:"email"`
	Phone          string            `json:"phone"`
	Address        string            `json:"address"`
	Hours          string            `json:"hours,omitempty"`
	Socials        map[string]string `json:"socials,omitempty"`
	ColorPrimary   string            `json:"color_primary,omitempty"`
	ColorSecondary string            `json:"color_secondary,omitempty"`
	Notes          string            `json:"notes"`
	Language       string            `json:"language"`
}

// Draft is one client's customisation of one theme's demo.
type Draft struct {
	Finished bool              `json:"finished,omitempty"` // the user saved it as done (step 4)
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Theme    string            `json:"theme"`
	Created  time.Time         `json:"created"`
	Updated  time.Time         `json:"updated"`
	Brief    Brief             `json:"brief"`
	Values   map[string]string `json:"values"` // field path → new text
	Images   map[string]string `json:"images"` // image slot id → file in the draft folder
	Logo     string            `json:"logo,omitempty"`
	// Uploads are the client's photos for the image slots (v2); Slots says which photo each slot
	// shows and Images holds the processed file Build packs.
	Uploads []Upload            `json:"uploads,omitempty"`
	Slots   map[string]SlotPick `json:"slots,omitempty"`
	// Products is the products mode of the run (keep | ai; "" = the theme's default).
	Products string `json:"products,omitempty"`

	Status    string `json:"status"` // "" | generating | ready | error
	Error     string `json:"error,omitempty"`
	Progress  string `json:"progress,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Tokens    int    `json:"tokens,omitempty"`
	Generated int    `json:"generated,omitempty"` // fields the AI wrote

	Dir string `json:"-"`
}

// Store keeps drafts in <config>/drafts/<id>/draft.json.
type Store struct {
	Dir string
	mu  sync.Mutex // file access
	umu sync.Mutex // whole read-modify-write in Update
}

var stores sync.Map

// Open returns the draft store for dir (one per dir, so locks are shared).
func Open(dir string) *Store {
	s, _ := stores.LoadOrStore(dir, &Store{Dir: dir})
	return s.(*Store)
}

var idRe = regexp.MustCompile(`^[a-f0-9]{12}$`)

// New creates a draft for a theme.
func (s *Store) New(theme, title string) (*Draft, error) {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	d := &Draft{ID: hex.EncodeToString(b), Theme: theme, Title: strings.TrimSpace(title), Created: time.Now(),
		Values: map[string]string{}, Images: map[string]string{}}
	d.Brief.SiteName = d.Title
	d.Dir = filepath.Join(s.Dir, d.ID)
	if err := os.MkdirAll(d.Dir, 0o700); err != nil {
		return nil, err
	}
	return d, s.Save(d)
}

// Get loads a draft.
func (s *Store) Get(id string) (*Draft, error) {
	if !idRe.MatchString(id) {
		return nil, errors.New("پیش‌نویس پیدا نشد")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(s.Dir, id, "draft.json"))
	if err != nil {
		return nil, errors.New("پیش‌نویس پیدا نشد")
	}
	var d Draft
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	d.Dir = filepath.Join(s.Dir, id)
	if d.Values == nil {
		d.Values = map[string]string{}
	}
	if d.Images == nil {
		d.Images = map[string]string{}
	}
	return &d, nil
}

// Save writes a draft.
func (s *Store) Save(d *Draft) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d.Updated = time.Now()
	b, _ := json.MarshalIndent(d, "", "  ")
	p := filepath.Join(s.Dir, d.ID, "draft.json")
	if err := os.WriteFile(p+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// Update loads, changes and saves a draft under the store lock order.
func (s *Store) Update(id string, fn func(*Draft) error) (*Draft, error) {
	s.umu.Lock()
	defer s.umu.Unlock()
	d, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if err := fn(d); err != nil {
		return nil, err
	}
	return d, s.Save(d)
}

// List returns drafts, newest first.
func (s *Store) List() []*Draft {
	entries, _ := os.ReadDir(s.Dir)
	var out []*Draft
	for _, e := range entries {
		if d, err := s.Get(e.Name()); err == nil {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// Delete removes a draft and its files.
func (s *Store) Delete(id string) error {
	if !idRe.MatchString(id) {
		return errors.New("invalid id")
	}
	return os.RemoveAll(filepath.Join(s.Dir, id))
}

// MaxImage is the largest image a client may upload.
const MaxImage = 10 << 20

var imageExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif"}

// SaveImage stores an uploaded image as name+ext in the draft folder and returns the file name.
func (d *Draft) SaveImage(name string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("فایل خالی است")
	}
	if len(data) > MaxImage {
		return "", fmt.Errorf("حجم تصویر بیش از %d مگابایت است", MaxImage>>20)
	}
	ext, ok := imageExt[http.DetectContentType(data)]
	if !ok {
		return "", errors.New("فقط تصویر png، jpg، webp یا gif پذیرفته می‌شود")
	}
	file := name + ext
	for _, old := range []string{".png", ".jpg", ".webp", ".gif"} {
		_ = os.Remove(filepath.Join(d.Dir, name+old))
	}
	if err := os.WriteFile(filepath.Join(d.Dir, file), data, 0o600); err != nil {
		return "", err
	}
	return file, nil
}

// File returns the path of a stored file (images, logo, built package).
func (d *Draft) File(name string) string { return filepath.Join(d.Dir, filepath.Base(name)) }
