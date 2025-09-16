package mock_gcs // nolint:revive // Nothing wrong with underscore in a name

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
	v1 "google.golang.org/api/storage/v1"
)

// Server is a mock Google Cloud Storage server for testing.
type Server struct {
	m      sync.Mutex
	data   map[string]*v1.Object
	bucket string
	server *httptest.Server

	failOnObjectExistence bool
	failOnObjectName      *string
}

// Opt is a function type for configuring the mock server.
type Opt func(*Server)

// WithFailOnObjectExistence configures the server to fail when objects already exist.
func WithFailOnObjectExistence() Opt {
	return func(s *Server) {
		s.failOnObjectExistence = true
	}
}

// WithFailOnObjectName configures the server to fail on operations with the specified object name.
func WithFailOnObjectName(name string) Opt {
	return func(s *Server) {
		s.failOnObjectName = &name
	}
}

// NewServer creates a new mock Google Cloud Storage server.
func NewServer(bucket string, opts ...Opt) *Server {
	server := &Server{
		m:                     sync.Mutex{},
		data:                  map[string]*v1.Object{},
		bucket:                bucket,
		failOnObjectExistence: false,
	}

	for _, opt := range opts {
		opt(server)
	}

	mux := http.NewServeMux()
	mux.Handle("POST /upload/storage/v1/b/{bucket}/o", server.validateRequest(server.createObject))
	mux.Handle("GET /b/{bucket}/o/{object...}", server.validateRequest(server.readObject))
	mux.Handle("DELETE /b/{bucket}/o/{object...}", server.validateRequest(server.deleteObject))
	mux.Handle("PATCH /b/{bucket}/o/{object...}", server.validateRequest(server.updateObject))
	mux.Handle("GET /b/{bucket}/o", server.validateRequest(server.listObjects))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, fmt.Sprintf("%s %s not handled", r.Method, r.URL.Path), 550)
	})
	server.server = httptest.NewUnstartedServer(mux)
	return server
}

// Close shuts down the mock server.
func (s *Server) Close() {
	s.server.Close()
}

// Client returns a Google Cloud Storage client configured to use this mock server.
func (s *Server) Client(ctx context.Context) (*storage.Client, error) {
	s.server.StartTLS()

	return storage.NewClient(ctx, option.WithHTTPClient(s.server.Client()), option.WithoutAuthentication(), option.WithEndpoint(s.server.URL))
}

// Add adds an object to the mock server's storage.
func (s *Server) Add(name string, attrs storage.ObjectAttrs) { // nolint:gocritic
	s.m.Lock()
	defer s.m.Unlock()

	s.data[name] = &v1.Object{
		Id:             name,
		Kind:           "storage#object",
		Metadata:       attrs.Metadata,
		Metageneration: attrs.Metageneration,
		Name:           name,
		Bucket:         s.bucket,
		CacheControl:   attrs.CacheControl,
		Generation:     attrs.Generation,
	}
}

// Get retrieves an object's attributes from the mock server's storage.
func (s *Server) Get(name string) *storage.ObjectAttrs {
	s.m.Lock()
	defer s.m.Unlock()

	obj, ok := s.data[name]
	if !ok {
		return nil
	}

	return &storage.ObjectAttrs{
		Bucket:         obj.Bucket,
		Name:           obj.Name,
		CacheControl:   obj.CacheControl,
		Metadata:       obj.Metadata,
		Generation:     obj.Generation,
		Metageneration: obj.Metageneration,
	}
}

// RemoveAll removes all objects from the mock server's storage.
func (s *Server) RemoveAll() {
	s.m.Lock()
	defer s.m.Unlock()

	s.data = map[string]*v1.Object{}
}

func (s *Server) validateRequest(next func(http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("bucket") != s.bucket {
			http.Error(w, "incorrect bucket", 599)
			return
		}
		http.HandlerFunc(next).ServeHTTP(w, r)
	})
}

func (s *Server) listObjects(w http.ResponseWriter, r *http.Request) {
	s.m.Lock()
	defer s.m.Unlock()

	prefix := r.URL.Query().Get("prefix")

	objs := v1.Objects{}

	for name, o := range s.data {
		if strings.HasPrefix(name, prefix) {
			objs.Items = append(objs.Items, o)
		}
	}

	w.WriteHeader(200)
	if err := json.NewEncoder(w).Encode(objs); err != nil {
		panic(err)
	}
}

func (s *Server) createObject(w http.ResponseWriter, r *http.Request) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, fmt.Sprintf("createObject failed to content type: %s", err), 599)
		return
	}

	reader := multipart.NewReader(r.Body, params["boundary"])
	defer func() {
		_ = r.Body.Close()
	}()

	jsonPart, err := reader.NextPart()
	if err != nil {
		http.Error(w, fmt.Sprintf("createObject failed to read body: %s", err), 599)
		return
	}

	var objectAttrs storage.ObjectAttrs
	if err := json.NewDecoder(jsonPart).Decode(&objectAttrs); err != nil {
		http.Error(w, fmt.Sprintf("createObject failed to read body: %s", err), 599)
		return
	}

	if objectAttrs.Name == "" {
		http.Error(w, "createObject missing name", http.StatusNotImplemented)
		return
	}

	if s.failOnObjectName != nil && objectAttrs.Name == *s.failOnObjectName {
		http.Error(w, "createObject failed on name", http.StatusTeapot)
		return
	}

	if s.failOnObjectExistence {
		query := r.URL.Query()
		if !query.Has("ifGenerationMatch") || query.Get("ifGenerationMatch") != "0" {
			http.Error(w, "createObject missing ifGenerationMatch", http.StatusNotImplemented)
			return
		}

		if _, ok := s.data[objectAttrs.Name]; ok {
			http.Error(w, "createObject already has that object", http.StatusPreconditionFailed)
			return
		}
	}

	object := v1.Object{
		Generation:     1,
		Id:             "doo",
		Kind:           "storage#object",
		Metadata:       objectAttrs.Metadata,
		Metageneration: 1,
		Name:           objectAttrs.Name,
		CacheControl:   objectAttrs.CacheControl,
		Bucket:         objectAttrs.Bucket,
	}

	s.m.Lock()
	defer s.m.Unlock()
	s.data[object.Name] = &object

	w.WriteHeader(200)
	if err := json.NewEncoder(w).Encode(object); err != nil {
		panic(err)
	}
}

func (s *Server) readObject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("object")

	s.m.Lock()
	defer s.m.Unlock()

	obj, ok := s.data[name]
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.WriteHeader(200)
	if err := json.NewEncoder(w).Encode(obj); err != nil {
		panic(err)
	}
}

func (s *Server) updateObject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("object")

	if s.failOnObjectName != nil && name == *s.failOnObjectName {
		http.Error(w, "updateObject failed on name", http.StatusTeapot)
		return
	}

	query := r.URL.Query()
	if !query.Has("ifMetagenerationMatch") {
		http.Error(w, "updateObject missing ifGenerationMatch", http.StatusNotImplemented)
		return
	}

	s.m.Lock()
	defer s.m.Unlock()

	obj, ok := s.data[name]
	if !ok {
		http.NotFound(w, r)
		return
	}

	if strconv.FormatInt(obj.Metageneration, 10) != query.Get("ifMetagenerationMatch") {
		http.Error(w, "updateObject with old metageneration", http.StatusPreconditionFailed)
		return
	}

	var objectAttrs storage.ObjectAttrs
	if err := json.NewDecoder(r.Body).Decode(&objectAttrs); err != nil {
		http.Error(w, fmt.Sprintf("createObject failed to read body: %s", err), 599)
		return
	}

	obj.Metadata = objectAttrs.Metadata
	obj.Metageneration++

	w.WriteHeader(200)
	if err := json.NewEncoder(w).Encode(obj); err != nil {
		panic(err)
	}
}

func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("object")

	if s.failOnObjectName != nil && name == *s.failOnObjectName {
		http.Error(w, "deleteObject failed on name", http.StatusTeapot)
		return
	}

	s.m.Lock()
	defer s.m.Unlock()

	query := r.URL.Query()
	if s.failOnObjectExistence {
		if !query.Has("ifMetagenerationMatch") {
			http.Error(w, "deleteObject missing ifGenerationMatch", 501)
			return
		}
	}

	obj, ok := s.data[name]
	if !ok {
		http.NotFound(w, r)
		return
	}

	if s.failOnObjectExistence {
		if strconv.FormatInt(obj.Metageneration, 10) != query.Get("ifMetagenerationMatch") {
			http.Error(w, "deleteObject with old metageneration", http.StatusPreconditionFailed)
			return
		}
	}

	delete(s.data, name)

	w.WriteHeader(204)
}
