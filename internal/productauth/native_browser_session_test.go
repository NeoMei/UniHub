package productauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"unihub/internal/accounts"
)

func TestNativeBrowserSourceLogoutIsSelective(t *testing.T) {
	s, handler, clients, _ := setupBrowserHandoffUI(t)
	s.ConfigureBrowserClients(clients)
	ctx := context.Background()

	first := nativeBrowserTestActor(t, s, "first-source")
	second := nativeBrowserTestActor(t, s, "second-source")
	const client = "wiki-native"
	sharedID := strings.Repeat("a", 64)
	otherID := strings.Repeat("b", 64)
	preservedID := strings.Repeat("c", 64)
	expiry := time.Now().Add(time.Hour).Unix()

	open := func(actor Identity, clientID, session string) *http.Cookie {
		t.Helper()
		path, cookie := approveNativeBrowserTestHandoff(t, s, handler, actor, clientID, session)
		response := handoffUIRequest(t, handler, http.StatusSeeOther, http.MethodGet, path.RequestURI(), nil, cookie)
		return handoffUICookie(t, response, "unihub_session")
	}
	revoked := open(first, client, sharedID)
	unrelated := open(first, client, otherID)
	untagged := open(first, "wiki", "")
	otherBinding := open(second, client, sharedID)
	pending, pendingCookie := approveNativeBrowserTestHandoff(t, s, handler, first, client, sharedID)

	const centralIssuer = "https://central.test"
	_, err := s.pool.Exec(ctx, `INSERT INTO identities(issuer,subject,user_id) VALUES($1,'central-alias',$2)`,
		centralIssuer, first.Binding.UserID)
	if err != nil {
		t.Fatal(err)
	}
	independent := &http.Cookie{Name: "unihub_session", Value: browserSecret()}
	_, err = s.pool.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at)
		VALUES($1,$2,'central-alias','Central','','csrf',now()+interval '10 minutes')`,
		browserSessionDigest(independent.Value), centralIssuer)
	if err != nil {
		t.Fatal(err)
	}
	path, cookie := approveNativeBrowserTestHandoff(t, s, handler, first, client, preservedID)
	preserved := handoffUIRequest(t, handler, http.StatusSeeOther, http.MethodGet, path.RequestURI(), nil, cookie, independent)
	if len(preserved.Result().Cookies()) != 1 || handoffUICookie(t, preserved, browserHandoffCookie).MaxAge >= 0 {
		t.Fatal("same-account handoff replaced the independent UniHub session")
	}

	for _, revokedID := range []string{sharedID, preservedID} {
		err := s.RevokeNativeBrowserSession(ctx, "agentwiki", "default", NativeSessionRevocation{
			LocalUserID: first.Binding.LocalUserID, NativeSessionID: revokedID, ExpiresAt: expiry,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.currentBrowser(ctx, revoked.Value); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("revoked browser session survived: %v", err)
	}
	handoffUIRequest(t, handler, http.StatusUnauthorized, http.MethodGet, pending.RequestURI(), nil, pendingCookie)
	for _, other := range []*http.Cookie{unrelated, untagged, otherBinding, independent} {
		if _, err := s.currentBrowser(ctx, other.Value); err != nil {
			t.Fatalf("selective logout revoked an unrelated session: %v", err)
		}
	}
}

func TestNativeBrowserHandoffRechecksSourceBeforeIssuance(t *testing.T) {
	s, handler, clients, _ := setupBrowserHandoffUI(t)
	s.ConfigureBrowserClients(clients)
	ctx := context.Background()
	actor := nativeBrowserTestActor(t, s, "inflight-source")
	session := strings.Repeat("d", 64)
	commit := func(clientID, session string) BrowserCompletion {
		t.Helper()
		path, cookie := approveNativeBrowserTestHandoff(t, s, handler, actor, clientID, session)
		out, err := s.FinishBrowserHandoff(ctx, path.Query().Get("handoff"), path.Query().Get("state"), cookie.Value)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	assertNoCookie := func(completion BrowserCompletion) {
		t.Helper()
		w := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "https://hub.test/auth/product/finish", nil)
		if s.writeBrowserHandoffSession(w, request, completion, nil) || w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
			t.Fatalf("stale handoff issued a browser session: HTTP %d", w.Code)
		}
	}

	consumed := commit("wiki-native", session)
	err := s.RevokeNativeBrowserSession(ctx, "agentwiki", "default", NativeSessionRevocation{
		LocalUserID: actor.Binding.LocalUserID, NativeSessionID: session, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNoCookie(consumed)

	legacy := commit("wiki", "")
	_, err = s.SyncSourceLifecycle(ctx, "agentwiki", "default", SourceLifecycle{
		Lifecycle: Lifecycle{LocalUserID: actor.Binding.LocalUserID, OperationID: "lock-inflight-source", PreviousAuthVersion: 1, NextAuthVersion: 2},
		State:     SourceStateLocked,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNoCookie(legacy)
}

func nativeBrowserTestActor(t *testing.T, s *Service, local string) Identity {
	t.Helper()
	ctx := context.Background()
	_, err := s.Register(ctx, "agentwiki", "default", Registration{
		LocalUserID: local, OperationID: "register-" + local, DisplayName: local, AuthVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := s.Authenticate(ctx, "agentwiki", "default", Authentication{
		LocalUserID: local, AuthenticationID: "auth-" + local, Method: "session",
		AuthenticatedAt: time.Now().Unix(), AuthVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func approveNativeBrowserTestHandoff(t *testing.T, s *Service, handler http.Handler, actor Identity, client, session string) (*url.URL, *http.Cookie) {
	t.Helper()
	state := browserSecret()
	start := handoffUIRequest(t, handler, http.StatusSeeOther, http.MethodGet, "/auth/product/start?client_id="+client+"&state="+state, nil)
	callback, err := start.Result().Location()
	if err != nil {
		t.Fatal(err)
	}
	var expiry int64
	var generation string
	if session != "" {
		expiry = time.Now().Add(time.Hour).Unix()
		registered, err := s.BrowserClient(client, "")
		if err != nil {
			t.Fatal(err)
		}
		generation = registered.NativeHandoffGeneration
	}
	receipt, err := s.ApproveBrowserHandoff(context.Background(), actor.ActorToken, actor.Binding.ProductID, actor.Binding.InstanceID, BrowserApproval{
		ClientID: client, HandoffID: callback.Query().Get("handoff"), State: state,
		NativeSessionID: session, NativeSessionExpiresAt: expiry, NativeHandoffGeneration: generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	path, err := url.Parse(receipt.FinishURL)
	if err != nil {
		t.Fatal(err)
	}
	return path, handoffUICookie(t, start, browserHandoffCookie)
}

func TestNativeBrowserHandoffAndLogoutSerialize(t *testing.T) {
	s, handler, clients, _ := setupBrowserHandoffUI(t)
	s.ConfigureBrowserClients(clients)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	actor := nativeBrowserTestActor(t, s, "concurrent-browser-source")
	session := strings.Repeat("e", 64)
	path, cookie := approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki-native", session)
	completion, err := s.FinishBrowserHandoff(ctx, path.Query().Get("handoff"), path.Query().Get("state"), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}

	lock, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if err = lockNativeBrowserSession(ctx, lock, actor.Binding.ID, session); err != nil {
		t.Fatal(err)
	}

	issued := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "https://hub.test/auth/product/finish", nil).WithContext(ctx)
		s.writeBrowserHandoffSession(response, request, completion, nil)
		issued <- response
	}()
	revoked := make(chan error, 1)
	go func() {
		revoked <- s.RevokeNativeBrowserSession(ctx, "agentwiki", "default", NativeSessionRevocation{
			LocalUserID: actor.Binding.LocalUserID, NativeSessionID: session, ExpiresAt: time.Now().Add(time.Hour).Unix(),
		})
	}()

	// Both operations must wait on the held key. Whichever wins after release,
	// logout must leave no live browser SID from the revoked native session.
	for {
		var waiting int
		err = lock.QueryRow(ctx, `SELECT count(*) FROM pg_locks w
			JOIN pg_locks h USING(locktype,database,classid,objid,objsubid)
			WHERE h.pid=pg_backend_pid() AND h.locktype='advisory' AND h.granted AND NOT w.granted`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
		select {
		case response := <-issued:
			t.Fatalf("browser issuance bypassed the lock: HTTP %d", response.Code)
		case err := <-revoked:
			t.Fatalf("logout bypassed the lock: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err = lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-revoked; err != nil {
		t.Fatal(err)
	}
	response := <-issued
	switch response.Code {
	case http.StatusOK:
		cookie := handoffUICookie(t, response, "unihub_session")
		if _, err = s.currentBrowser(ctx, cookie.Value); !errors.Is(err, accounts.ErrForbidden) {
			t.Fatalf("logout left the concurrent browser session usable: %v", err)
		}
	case http.StatusUnauthorized:
		if len(response.Result().Cookies()) != 0 {
			t.Fatal("revoked handoff wrote a browser cookie")
		}
	default:
		t.Fatalf("unexpected issuance result: HTTP %d", response.Code)
	}
}

func TestNativeBrowserHandoffRejectsExpiredJWT(t *testing.T) {
	s, handler, clients, _ := setupBrowserHandoffUI(t)
	s.ConfigureBrowserClients(clients)
	ctx := context.Background()
	actor := nativeBrowserTestActor(t, s, "expired-jwt-source")
	session := strings.Repeat("f", 64)
	path, cookie := approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki-native", session)
	if _, err := s.pool.Exec(ctx, `UPDATE product_browser_handoffs
		SET native_session_expires_at=clock_timestamp()-interval '1 second' WHERE id_hash=$1`,
		digest(path.Query().Get("handoff"))); err != nil {
		t.Fatal(err)
	}
	response := handoffUIRequest(t, handler, http.StatusUnauthorized, http.MethodGet, path.RequestURI(), nil, cookie)
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("expired native approval wrote a browser cookie")
	}

	path, cookie = approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki-native", session)
	completion, err := s.FinishBrowserHandoff(ctx, path.Query().Get("handoff"), path.Query().Get("state"), cookie.Value)
	if err != nil || completion.NativeSessionExpiresAt == nil {
		t.Fatalf("valid handoff lost its native expiry: %+v, %v", completion, err)
	}
	// Model the expiry passing between handoff redemption and the session write.
	expired := time.Now().Add(-time.Second)
	completion.NativeSessionExpiresAt = &expired
	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "https://hub.test/auth/product/finish", nil)
	if s.writeBrowserHandoffSession(response, request, completion, nil) || response.Code != http.StatusUnauthorized || len(response.Result().Cookies()) != 0 {
		t.Fatalf("expired redeemed handoff wrote a browser session: HTTP %d", response.Code)
	}
}

func TestNativeBrowserHandoffRecoversFromSelectiveSignOutCookie(t *testing.T) {
	s, handler, clients, _ := setupBrowserHandoffUI(t)
	s.ConfigureBrowserClients(clients)
	ctx := context.Background()
	actor := nativeBrowserTestActor(t, s, "fresh-browser-handoff")
	oldID := strings.Repeat("a", 64)
	staleID := strings.Repeat("d", 64)
	if _, err := s.pool.Exec(ctx, `INSERT INTO product_browser_native_session_revocations
		(binding_id,native_session_id,expires_at) VALUES($1,$2,clock_timestamp()-interval '1 minute')`,
		actor.Binding.ID, staleID); err != nil {
		t.Fatal(err)
	}
	path, binding := approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki-native", oldID)
	signedIn := handoffUIRequest(t, handler, http.StatusSeeOther, http.MethodGet, path.RequestURI(), nil, binding)
	old := handoffUICookie(t, signedIn, "unihub_session")

	if err := s.RevokeNativeBrowserSession(ctx, "agentwiki", "default", NativeSessionRevocation{
		LocalUserID: actor.Binding.LocalUserID, NativeSessionID: oldID, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	var expiredKept, activeKept bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product_browser_native_session_revocations
		WHERE binding_id=$1 AND native_session_id=$2), EXISTS(SELECT 1 FROM product_browser_native_session_revocations
		WHERE binding_id=$1 AND native_session_id=$3)`, actor.Binding.ID, staleID, oldID).Scan(&expiredKept, &activeKept); err != nil || expiredKept || !activeKept {
		t.Fatalf("expected only the active revocation to remain, expired=%v active=%v err=%v", expiredKept, activeKept, err)
	}
	if _, err := s.currentBrowser(ctx, old.Value); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("selective sign-out did not revoke the old session: %v", err)
	}

	// Back-channel sign-out leaves the old cookie in the browser. A handoff
	// with a newly authenticated native session can replace it exactly once.
	path, binding = approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki-native", strings.Repeat("b", 64))
	refreshed := handoffUIRequest(t, handler, http.StatusSeeOther, http.MethodGet, path.RequestURI(), nil, old, binding)
	fresh := handoffUICookie(t, refreshed, "unihub_session")
	if fresh.Value == old.Value {
		t.Fatal("fresh handoff reused the revoked cookie")
	}
	if _, user, err := s.browserSessionIdentity(ctx, fresh.Value); err != nil || user != actor.Binding.UserID {
		t.Fatalf("fresh handoff created the wrong browser session: %q, %v", user, err)
	}

	path, binding = approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki-native", strings.Repeat("c", 64))
	stale := handoffUIRequest(t, handler, http.StatusConflict, http.MethodGet, path.RequestURI(), nil, old, binding)
	if len(stale.Result().Cookies()) != 0 {
		t.Fatal("a second handoff rewrote the browser cookie through the old sign-out")
	}
	if _, err := s.currentBrowser(ctx, fresh.Value); err != nil {
		t.Fatalf("stale request revoked the new browser session: %v", err)
	}
}

func TestNativeBrowserHandoffKeepsLogoutRecoveryWhenNewSessionExpiresAtCommit(t *testing.T) {
	s, handler, clients, _ := setupBrowserHandoffUI(t)
	s.ConfigureBrowserClients(clients)
	ctx := context.Background()
	actor := nativeBrowserTestActor(t, s, "browser-expiry-at-commit")
	oldID := strings.Repeat("a", 64)
	path, binding := approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki-native", oldID)
	signedIn := handoffUIRequest(t, handler, http.StatusSeeOther, http.MethodGet, path.RequestURI(), nil, binding)
	old := handoffUICookie(t, signedIn, "unihub_session")
	if err := s.RevokeNativeBrowserSession(ctx, "agentwiki", "default", NativeSessionRevocation{
		LocalUserID: actor.Binding.LocalUserID, NativeSessionID: oldID, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	oldHash := browserSessionDigest(old.Value)
	markerExpiry := func() time.Time {
		t.Helper()
		var expiry time.Time
		if err := s.pool.QueryRow(ctx, `SELECT expires_at FROM product_browser_native_logout_cookies WHERE session_id_hash=$1`,
			oldHash).Scan(&expiry); err != nil {
			t.Fatalf("missing logout recovery record: %v", err)
		}
		return expiry
	}
	wantExpiry := markerExpiry()

	// Delay COMMIT for the short-lived session after its INSERT succeeds.
	// The observation row proves the deferred trigger ran and committed.
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE test_browser_expired_at_commit (session_id_hash text NOT NULL);
		CREATE FUNCTION test_browser_expire_at_commit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.expires_at<clock_timestamp()+interval '10 seconds' THEN
				PERFORM pg_sleep_until(NEW.expires_at+interval '50 milliseconds');
				INSERT INTO test_browser_expired_at_commit VALUES(NEW.id_hash);
			END IF;
			RETURN NEW;
		END
		$$;
		CREATE CONSTRAINT TRIGGER test_browser_expire_at_commit AFTER INSERT ON browser_sessions
			DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION test_browser_expire_at_commit()
	`); err != nil {
		t.Fatal(err)
	}

	path, binding = approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki-native", strings.Repeat("b", 64))
	if _, err := s.pool.Exec(ctx, `UPDATE product_browser_handoffs
		SET native_session_expires_at=date_trunc('second',clock_timestamp())+interval '4 seconds'
		WHERE id_hash=$1`, digest(path.Query().Get("handoff"))); err != nil {
		t.Fatal(err)
	}
	failed := handoffUIRequest(t, handler, http.StatusUnauthorized, http.MethodGet, path.RequestURI(), nil, old, binding)
	if len(failed.Result().Cookies()) != 0 {
		t.Fatal("expired handoff sent a cookie")
	}
	var observed int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM test_browser_expired_at_commit`).Scan(&observed); err != nil || observed != 1 {
		t.Fatalf("expected one session to expire during commit, got %d (%v)", observed, err)
	}
	if got := markerExpiry(); !got.Equal(wantExpiry) {
		t.Fatalf("logout recovery expiry changed: got %s, want %s", got, wantExpiry)
	}
	var unused int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM browser_sessions b
		JOIN test_browser_expired_at_commit o ON o.session_id_hash=b.id_hash`).Scan(&unused); err != nil || unused != 0 {
		t.Fatalf("expired unpublished session survived: %d (%v)", unused, err)
	}

	path, binding = approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki-native", strings.Repeat("c", 64))
	fresh := handoffUIRequest(t, handler, http.StatusSeeOther, http.MethodGet, path.RequestURI(), nil, old, binding)
	winner := handoffUICookie(t, fresh, "unihub_session")
	if _, user, err := s.browserSessionIdentity(ctx, winner.Value); err != nil || user != actor.Binding.UserID {
		t.Fatalf("retry did not issue a valid browser session: %q, %v", user, err)
	}

	path, binding = approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki-native", strings.Repeat("d", 64))
	stale := handoffUIRequest(t, handler, http.StatusConflict, http.MethodGet, path.RequestURI(), nil, old, binding)
	if len(stale.Result().Cookies()) != 0 {
		t.Fatal("retry through the consumed recovery record rewrote the cookie")
	}
}
