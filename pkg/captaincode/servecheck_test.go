package captaincode

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A serve that answers /config/providers with an EMPTY list cannot host a
// session (a test's throwaway HOME started it); one that does not answer
// the question at all is given the benefit of the doubt.
func TestServeWithNoProvidersIsNotAdopted(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"providers":[]}`)) }))
	defer empty.Close()
	d := &OpencodeDispatcher{BaseURL: empty.URL, Client: empty.Client(), Spawn: false}
	assert.False(t, d.serveHasProviders())
	err := d.EnsureServer()
	assert.ErrorContains(t, err, "no providers configured")

	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"providers":[{"id":"openrouter"}]}`)) }))
	defer real.Close()
	d = &OpencodeDispatcher{BaseURL: real.URL, Client: real.Client(), Spawn: false}
	assert.True(t, d.serveHasProviders())
	assert.NoError(t, d.EnsureServer())

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"id":"ses_1"}`)) }))
	defer fake.Close()
	d = &OpencodeDispatcher{BaseURL: fake.URL, Client: fake.Client(), Spawn: false}
	assert.True(t, d.serveHasProviders(), "no providers answer: no verdict")
}
