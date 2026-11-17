package captaincode

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// This file is the hub half of the link: pairing with one hub, pinning that
// hub's signing key, and asking it whether the code the user typed on the web
// was approved. The brain still dials out only. Nothing here opens a listening
// socket, and every reply is checked before it changes local state.
//
// The URL must be https, with no credentials, query or fragment. The hub's
// certificate is verified against the system roots plus an optional local CA
// file, and its Ed25519 key is pinned the first time. A later reply under a
// different key is refused; only `captain link unpair` drops the pin.

const (
	LinkPairPending  = "pending"
	LinkPairApproved = "approved"
	LinkPairExpired  = "expired"

	linkBrainDirection = "brain_to_hub"
	linkHubDirection   = "hub_to_brain"

	linkMaxCA    = 256 * 1024
	linkPairPoll = 2 * time.Second
)

// linkPairCode is the hub's pairing code: eight unpadded base32 characters.
var linkPairCode = regexp.MustCompile(`^[A-Z2-7]{8}$`)

// LinkHub is one hub paired with this machine. The key is the hub's public key
// pinned over verified HTTPS. PairID and Code belong to the live pairing
// window; ApprovedAt is set once the hub says an account approved it. CAFile
// remembers where `pair --ca` found the hub's private certificate authority,
// so the brain's own stream verifies this hub the same way pairing did
// without being told again; it is local state and never travels.
type LinkHub struct {
	URL         string `json:"url"`
	PublicKey   string `json:"public_key"`
	PairID      string `json:"pair_id"`
	Code        string `json:"code"`
	RequestedAt int64  `json:"requested_at"`
	ExpiresAt   int64  `json:"expires_at"`
	ApprovedAt  int64  `json:"approved_at,omitempty"`
	CAFile      string `json:"ca_file,omitempty"`
}

// LinkHubState reads the pinned hub, if this machine paired with one.
func LinkHubState() (LinkHub, bool, error) {
	state, err := linkRead()
	if err != nil {
		return LinkHub{}, false, err
	}
	if state.Hub == nil {
		return LinkHub{}, false, nil
	}
	return *state.Hub, true, nil
}

// LinkHubFingerprint is the shareable form of the pinned hub key, in the same
// shape as the machine key's fingerprint, so both sides print alike.
func LinkHubFingerprint(pinned string) (string, error) {
	pub, err := base64.RawURLEncoding.DecodeString(pinned)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", fmt.Errorf("pinned hub key is not a link key")
	}
	return LinkFingerprint(ed25519.PublicKey(pub)), nil
}

// LinkApprovalURL is the page where the user types a pairing code: the hub's
// own link-management path. The code never travels in it.
func LinkApprovalURL(hubURL string) (string, error) {
	base, err := linkHubBase(hubURL)
	if err != nil {
		return "", err
	}
	return base + "/captains/pair/", nil
}

// LinkUnpair drops the local hub pin and the code of an unfinished pairing.
// It changes no hub record: revoking a paired machine stays a hub action.
func LinkUnpair() (bool, error) {
	cleared := false
	_, err := linkUpdate(true, func(s *linkState) error {
		if s.Hub != nil {
			s.Hub = nil
			cleared = true
		}
		return nil
	})
	return cleared, err
}

// linkHubBase validates a hub URL and returns it without a trailing slash.
func linkHubBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("hub url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("hub url is not a url")
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("hub url must use https")
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("hub url has no host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("hub url must not carry credentials, a query or a fragment")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// linkEndpoint joins the hub base with a link protocol path. The path is part
// of the protocol, not a caller choice.
func linkEndpoint(base, suffix string) string {
	return base + "/api/v1/link" + suffix
}

// linkLabel is what this machine calls itself: the host name reduced to the
// characters the hub accepts, trimmed, and never empty.
func linkLabel() (string, error) {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "machine"
	}
	var kept strings.Builder
	for _, r := range host {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '.' || r == '_' || r == '-' {
			kept.WriteRune(r)
		}
	}
	label := strings.Trim(kept.String(), " \t._-")
	if label == "" {
		return "machine", nil
	}
	runes := []rune(label)
	if len(runes) > 80 {
		label = strings.Trim(string(runes[:80]), " \t._-")
	}
	if label == "" {
		return "machine", nil
	}
	return label, nil
}

// linkReadCA reads an extra certificate authority file. It rejects anything
// that is not one regular file of a bounded size, so a link cannot be steered
// through a device or a fifo.
func linkReadCA(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("hub CA file: %v", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("hub CA file must be a regular file")
	}
	if info.Size() <= 0 || info.Size() > linkMaxCA {
		return nil, fmt.Errorf("hub CA file must hold between 1 and %d bytes", linkMaxCA)
	}
	raw, err := io.ReadAll(io.LimitReader(f, linkMaxCA+1))
	if err != nil {
		return nil, fmt.Errorf("hub CA file: %v", err)
	}
	if len(raw) > linkMaxCA {
		return nil, fmt.Errorf("hub CA file is too large")
	}
	return raw, nil
}

// linkHTTPClient builds the one client this protocol uses: verified TLS to
// real roots plus an optional pinned local CA, a request timeout, and no way
// to switch verification off from a flag or an environment variable.
func linkHTTPClient(caFile string) (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	ca, err := linkReadCA(caFile)
	if err != nil {
		return nil, err
	}
	if len(ca) > 0 && !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("hub CA file holds no certificate")
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool},
			Proxy:           http.ProxyFromEnvironment,
		},
	}, nil
}

// linkPost sends one signed envelope and reads one signed reply. A non-200
// answer is read as the signed link.error the protocol defines, and every
// failure becomes one of the protocol's own messages.
func linkPost(ctx context.Context, client *http.Client, endpoint string, e LinkEnvelope) (LinkEnvelope, error) {
	body, err := json.Marshal(e)
	if err != nil {
		return LinkEnvelope{}, err
	}
	if len(body) > linkMaxBody {
		return LinkEnvelope{}, fmt.Errorf("link envelope is too large")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return LinkEnvelope{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return LinkEnvelope{}, fmt.Errorf("the hub did not answer: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, linkMaxBody+1))
	if err != nil {
		return LinkEnvelope{}, fmt.Errorf("the hub answer could not be read: %v", err)
	}
	if len(raw) > linkMaxBody {
		return LinkEnvelope{}, fmt.Errorf("the hub answer is too large")
	}
	var reply LinkEnvelope
	decodeErr := LinkDecodeEnvelope(raw, &reply)
	if resp.StatusCode != http.StatusOK {
		if decodeErr == nil && reply.Type == "link.error" {
			var pay struct {
				Status int `json:"status"`
			}
			if LinkDecodePayload(reply, &pay) == nil {
				return LinkEnvelope{}, linkHubError(pay.Status)
			}
		}
		return LinkEnvelope{}, linkHubError(resp.StatusCode)
	}
	if decodeErr != nil {
		return LinkEnvelope{}, fmt.Errorf("the hub answer is not a link envelope")
	}
	return reply, nil
}

// linkHubError turns a protocol status into one actionable sentence. The
// numbers come from the spec, not from the answer body.
func linkHubError(status int) error {
	switch status {
	case 400:
		return fmt.Errorf("the hub refused the message as malformed")
	case 401:
		return fmt.Errorf("the hub rejected the signature")
	case 403:
		return fmt.Errorf("the hub refused this machine: it is not paired, the pairing window closed, or the link was revoked")
	case 409:
		return fmt.Errorf("the hub already holds a live pairing for this machine; wait for it to expire, then pair again")
	case 503:
		return fmt.Errorf("the hub cannot sign right now; try again later")
	}
	return fmt.Errorf("the hub answered HTTP %d", status)
}

// LinkPair asks a hub for a pairing code. A live unfinished pairing with the
// same hub is reused instead of requested again, because the hub allows one
// pending pairing per machine. The hub's key is pinned with the reply and
// later compared before any signed answer is trusted.
func LinkPair(ctx context.Context, hubURL, caFile string) (LinkHub, bool, error) {
	base, err := linkHubBase(hubURL)
	if err != nil {
		return LinkHub{}, false, err
	}
	now := time.Now()
	stored, has, err := LinkHubState()
	if err != nil {
		return LinkHub{}, false, err
	}
	if has {
		if stored.URL != base {
			return stored, false, fmt.Errorf("this machine is already paired with %s; run captain link unpair first", stored.URL)
		}
		if stored.ApprovedAt > 0 {
			return stored, false, fmt.Errorf("this machine is already paired with %s; run captain link unpair to pair it again", base)
		}
		if stored.ExpiresAt > now.Unix() {
			return stored, true, nil
		}
	}
	machineID, err := LinkMachineID()
	if err != nil {
		return LinkHub{}, false, fmt.Errorf("machine identity is not configured: %v", err)
	}
	if _, err := EnsureLinkKey(); err != nil {
		return LinkHub{}, false, err
	}
	priv, err := LoadLinkKey()
	if err != nil {
		return LinkHub{}, false, err
	}
	label, err := linkLabel()
	if err != nil {
		return LinkHub{}, false, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	payload := struct {
		MachineID string `json:"machine_id"`
		PublicKey string `json:"public_key"`
		Label     string `json:"label"`
	}{
		MachineID: machineID,
		PublicKey: base64.RawURLEncoding.EncodeToString(pub),
		Label:     label,
	}
	request, err := LinkSign(priv, machineID, linkBrainDirection, "pair.request", payload, now)
	if err != nil {
		return LinkHub{}, false, err
	}
	client, err := linkHTTPClient(caFile)
	if err != nil {
		return LinkHub{}, false, err
	}
	reply, err := linkPost(ctx, client, linkEndpoint(base, "/pair"), request)
	if err != nil {
		return LinkHub{}, false, err
	}
	if reply.MachineID != machineID {
		return LinkHub{}, false, fmt.Errorf("the hub answered for another machine")
	}
	if reply.Type != "pair.pending" {
		return LinkHub{}, false, fmt.Errorf("the hub answered a message where pairing was expected")
	}
	var pending struct {
		Code         string    `json:"code"`
		PairID       string    `json:"pair_id"`
		ExpiresAt    time.Time `json:"expires_at"`
		HubPublicKey string    `json:"hub_public_key"`
	}
	if err := LinkDecodePayload(reply, &pending); err != nil {
		return LinkHub{}, false, fmt.Errorf("the hub pairing answer is malformed")
	}
	candidate, err := base64.RawURLEncoding.DecodeString(pending.HubPublicKey)
	if err != nil || len(candidate) != ed25519.PublicKeySize {
		return LinkHub{}, false, fmt.Errorf("the hub pairing answer has no link key")
	}
	key := ed25519.PublicKey(candidate)
	if has && stored.PublicKey != "" {
		pinned, err := base64.RawURLEncoding.DecodeString(stored.PublicKey)
		if err != nil || len(pinned) != ed25519.PublicKeySize {
			return LinkHub{}, false, fmt.Errorf("the pinned hub key is unreadable; run captain link unpair")
		}
		if !bytes.Equal(pinned, candidate) {
			return LinkHub{}, false, fmt.Errorf("the hub signed with a different key than the pinned one; run captain link unpair to trust a new hub")
		}
		key = ed25519.PublicKey(pinned)
	}
	if err := LinkVerify(key, reply, linkHubDirection, now); err != nil {
		return LinkHub{}, false, err
	}
	if !linkPairCode.MatchString(pending.Code) {
		return LinkHub{}, false, fmt.Errorf("the hub pairing answer has an unusable code")
	}
	if !linkIdentifier.MatchString(pending.PairID) || len(pending.PairID) < 20 {
		return LinkHub{}, false, fmt.Errorf("the hub pairing answer has an unusable pairing id")
	}
	if !pending.ExpiresAt.After(now.Add(-time.Minute)) {
		return LinkHub{}, false, fmt.Errorf("the pairing window on the hub is already closed")
	}
	hub := LinkHub{
		URL:         base,
		PublicKey:   pending.HubPublicKey,
		PairID:      pending.PairID,
		Code:        pending.Code,
		RequestedAt: now.Unix(),
		ExpiresAt:   pending.ExpiresAt.Unix(),
		CAFile:      caFile,
	}
	if _, err := linkUpdate(true, func(s *linkState) error {
		if s.Hub != nil && s.Hub.ApprovedAt > 0 && s.Hub.URL == base {
			return fmt.Errorf("this machine is already paired with %s", base)
		}
		stored := hub
		s.Hub = &stored
		return nil
	}); err != nil {
		return LinkHub{}, false, err
	}
	return hub, false, nil
}

// LinkPairStatus asks the hub whether a live pairing was approved. It reads
// the local answer first: an approved or expired window needs no network.
// The reply is checked against the pinned key; a key change never passes.
func LinkPairStatus(ctx context.Context, caFile string) (string, error) {
	stored, has, err := LinkHubState()
	if err != nil {
		return "", err
	}
	if !has {
		return "", fmt.Errorf("no hub is paired with this machine")
	}
	now := time.Now()
	if stored.ApprovedAt > 0 {
		return LinkPairApproved, nil
	}
	if stored.ExpiresAt <= now.Unix() {
		return LinkPairExpired, nil
	}
	machineID, err := LinkMachineID()
	if err != nil {
		return "", fmt.Errorf("machine identity is not configured: %v", err)
	}
	priv, err := LoadLinkKey()
	if err != nil {
		return "", err
	}
	payload := struct {
		PairID string `json:"pair_id"`
	}{PairID: stored.PairID}
	request, err := LinkSign(priv, machineID, linkBrainDirection, "pair.status", payload, now)
	if err != nil {
		return "", err
	}
	client, err := linkHTTPClient(caFile)
	if err != nil {
		return "", err
	}
	reply, err := linkPost(ctx, client, linkEndpoint(stored.URL, "/pair/status"), request)
	if err != nil {
		return "", err
	}
	pinned, err := base64.RawURLEncoding.DecodeString(stored.PublicKey)
	if err != nil || len(pinned) != ed25519.PublicKeySize {
		return "", fmt.Errorf("the pinned hub key is unreadable; run captain link unpair")
	}
	if reply.MachineID != machineID {
		return "", fmt.Errorf("the hub answered for another machine")
	}
	if reply.Type != "pair.status" {
		return "", fmt.Errorf("the hub answered a message where a pairing status was expected")
	}
	if err := LinkVerify(ed25519.PublicKey(pinned), reply, linkHubDirection, now); err != nil {
		return "", err
	}
	var pay struct {
		Status string `json:"status"`
	}
	if err := LinkDecodePayload(reply, &pay); err != nil {
		return "", fmt.Errorf("the hub status answer is malformed")
	}
	switch pay.Status {
	case LinkPairPending:
		return LinkPairPending, nil
	case LinkPairApproved:
		if _, err := linkUpdate(true, func(s *linkState) error {
			if s.Hub == nil || s.Hub.URL != stored.URL {
				return fmt.Errorf("the pairing changed while its status was read")
			}
			if s.Hub.ApprovedAt == 0 {
				s.Hub.ApprovedAt = now.Unix()
			}
			return nil
		}); err != nil {
			return "", err
		}
		return LinkPairApproved, nil
	}
	return "", fmt.Errorf("the hub answered an unknown pairing status")
}

// LinkPairWait polls a live pairing until the hub approves it, the window
// closes, the caller's context ends, or the deadline passes. It returns the
// last observed state; onStatus receives each observed state so a caller can
// show progress without timing the polls itself.
func LinkPairWait(ctx context.Context, caFile string, wait time.Duration, onStatus func(string)) (string, error) {
	deadline := time.Now().Add(wait)
	for {
		status, err := LinkPairStatus(ctx, caFile)
		if err != nil {
			return "", err
		}
		if onStatus != nil {
			onStatus(status)
		}
		if status == LinkPairApproved || status == LinkPairExpired {
			return status, nil
		}
		if wait <= 0 || !time.Now().Before(deadline) {
			return status, nil
		}
		delay := linkPairPoll
		if left := time.Until(deadline); left < delay {
			delay = left
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status, ctx.Err()
		case <-timer.C:
		}
	}
}
