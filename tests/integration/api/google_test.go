package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
	"hoanxu/internal/auth"
)

// The provider is local, but tokens are RSA-signed and verified by the real OIDC verifier.
func TestGoogleIdentityClaimsAndAccountIsolation(t *testing.T) {
	store, _, admin := testStore(t)
	ctx := context.Background()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if e != nil {
		t.Fatal(e)
	}
	var raw string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-access", "token_type": "Bearer", "id_token": raw})
	}))
	defer provider.Close()
	svc := &auth.Service{Store: store, OAuth: &oauth2.Config{ClientID: "local-client", ClientSecret: "fixture-secret", Endpoint: oauth2.Endpoint{AuthURL: provider.URL + "/authorize", TokenURL: provider.URL, AuthStyle: oauth2.AuthStyleInParams}}, Verifier: oidc.NewVerifier("https://accounts.google.com", &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: "local-client"})}
	_, e = store.Pool.Exec(ctx, `UPDATE users SET email='same@example.com' WHERE id=$1`, admin)
	if e != nil {
		t.Fatal(e)
	}
	start := func() (string, map[string]any) {
		location, state, e := svc.GoogleStart(ctx)
		if e != nil {
			t.Fatal(e)
		}
		u, e := url.Parse(location)
		if e != nil {
			t.Fatal(e)
		}
		return state, map[string]any{"iss": "https://accounts.google.com", "aud": "local-client", "sub": "google-subject", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": u.Query().Get("nonce"), "email": "same@example.com", "email_verified": true, "name": "Customer"}
	}
	finish := func(state string, claims map[string]any) (string, error) {
		b, _ := json.Marshal(claims)
		token, e := signer.Sign(b)
		if e != nil {
			t.Fatal(e)
		}
		raw, e = token.CompactSerialize()
		if e != nil {
			t.Fatal(e)
		}
		return svc.GoogleFinish(ctx, state, "fixture-code")
	}
	if _, e = svc.GoogleFinish(ctx, "invalid-state", "code"); e == nil {
		t.Fatal("invalid state accepted")
	}
	for _, tc := range []struct {
		name, field string
		value       any
	}{{"nonce", "nonce", "wrong"}, {"audience", "aud", "other-client"}, {"issuer", "iss", "https://attacker.invalid"}, {"expired", "exp", time.Now().Add(-time.Hour).Unix()}, {"email verification", "email_verified", false}, {"subject", "sub", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			state, claims := start()
			claims[tc.field] = tc.value
			if _, e = finish(state, claims); e == nil {
				t.Fatal("invalid claim accepted")
			}
		})
	}
	state, claims := start()
	wrongKey, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	wrongSigner, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: wrongKey}, nil)
	if e != nil {
		t.Fatal(e)
	}
	correctSigner := signer
	signer = wrongSigner
	if _, e = finish(state, claims); e == nil {
		t.Fatal("wrong signature accepted")
	}
	signer = correctSigner
	state, claims = start()
	token, e := finish(state, claims)
	if e != nil {
		t.Fatal(e)
	}
	customer, e := svc.Session(ctx, token)
	if e != nil || customer.Role != "customer" || customer.ID == admin {
		t.Fatal(customer, e)
	}
	var balance, coins int64
	e = store.Pool.QueryRow(ctx, `SELECT (SELECT sum(balance) FROM wallet_accounts WHERE user_id=$1),(SELECT balance FROM coin_accounts WHERE user_id=$1)`, customer.ID).Scan(&balance, &coins)
	if e != nil || balance != 0 || coins != 0 {
		t.Fatal(balance, coins, e)
	}
	if _, e = finish(state, claims); e == nil {
		t.Fatal("replayed state accepted")
	}
	state, claims = start()
	claims["email"] = "changed@example.com"
	token, e = finish(state, claims)
	if e != nil {
		t.Fatal(e)
	}
	same, e := svc.Session(ctx, token)
	if e != nil || same.ID != customer.ID {
		t.Fatal("subject did not retain identity", same, e)
	}
	_, e = store.Pool.Exec(ctx, `UPDATE users SET blocked=true WHERE id=$1`, customer.ID)
	if e != nil {
		t.Fatal(e)
	}
	state, claims = start()
	if _, e = finish(state, claims); e == nil {
		t.Fatal("blocked customer signed in")
	}
}
