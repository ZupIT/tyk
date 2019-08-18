package gateway

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/TykTechnologies/tyk/config"
	"github.com/TykTechnologies/tyk/test"
	"github.com/garyburd/redigo/redis"
)

func TestDeRegisterWithMutualTLS(t *testing.T) {
	_, _, combinedClientPEM, clientCert := test.GenCertificate(&x509.Certificate{})
	clientCert.Leaf, _ = x509.ParseCertificate(clientCert.Certificate[0])

	// Setup mutual TLS protected dashboard
	dashboard := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/system/node" {
			w.Write([]byte(`{"Status": "OK", "Nonce": "1", "Message": {"NodeID": "1"}}`))
		} else {
			t.Fatal("Unknown dashboard API request", r)
		}
	}))
	pool := x509.NewCertPool()
	pool.AddCert(clientCert.Leaf)
	dashboard.TLS = &tls.Config{
		ClientAuth:         tls.RequireAndVerifyClientCert,
		ClientCAs:          pool,
		InsecureSkipVerify: true,
	}

	dashboard.StartTLS()
	defer dashboard.Close()

	certID, _ := CertificateManager.Add(combinedClientPEM, "")
	defer CertificateManager.Delete(certID)

	getConfig := func(certID string) config.Config {
		cfg := config.Global()
		cfg.UseDBAppConfigs = true
		cfg.DBAppConfOptions.ConnectionString = dashboard.URL
		cfg.Security.Certificates.Dashboard = certID
		cfg.NodeSecret = "somesecret"
		// cfg.HttpServerOptions.UseSSL = true
		cfg.HttpServerOptions.SSLInsecureSkipVerify = true
		return cfg
	}

	var dashService DashboardServiceSender = &HTTPDashboardHandler{}

	t.Run("Without dashboard certificate", func(t *testing.T) {
		config.SetGlobal(getConfig(""))
		defer ResetTestConfig()

		dashService.Init()
		if err := dashService.DeRegister(); err == nil {
			t.Error("Should reject without certificate")
		}
	})

	t.Run("With dashboard certificate", func(t *testing.T) {
		config.SetGlobal(getConfig(certID))
		defer ResetTestConfig()

		dashService.Init()
		if err := dashService.DeRegister(); err != nil {
			t.Error("Should succeed with certificate")
		}
	})
}

func TestSyncAPISpecsDashboardSuccess(t *testing.T) {
	// Test Dashboard
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/system/apis" {
			w.Write([]byte(`{"Status": "OK", "Nonce": "1", "Message": [{"api_definition": {}}]}`))
		} else {
			t.Fatal("Unknown dashboard API request", r)
		}
	}))
	defer ts.Close()

	apisMu.Lock()
	apisByID = make(map[string]*APISpec)
	apisMu.Unlock()

	globalConf := config.Global()
	globalConf.UseDBAppConfigs = true
	globalConf.AllowInsecureConfigs = true
	globalConf.DBAppConfOptions.ConnectionString = ts.URL
	config.SetGlobal(globalConf)

	defer ResetTestConfig()

	var wg sync.WaitGroup
	wg.Add(1)
	msg := redis.Message{Data: []byte(`{"Command": "ApiUpdated"}`)}
	handled := func(got NotificationCommand) {
		if want := NoticeApiUpdated; got != want {
			t.Fatalf("want %q, got %q", want, got)
		}
	}
	handleRedisEvent(msg, handled, wg.Done)

	// Since we already know that reload is queued
	ReloadTick <- time.Time{}

	// Wait for the reload to finish, then check it worked
	wg.Wait()
	apisMu.RLock()
	if len(apisByID) != 1 {
		t.Error("Should return array with one spec", apisByID)
	}
	apisMu.RUnlock()
}

func TestSyncAPISpecsDashboardJSONFailure(t *testing.T) {
	// Test Dashboard
	callNum := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/system/apis" {
			if callNum == 0 {
				w.Write([]byte(`{"Status": "OK", "Nonce": "1", "Message": [{"api_definition": {}}]}`))
			} else {
				w.Write([]byte(`{"Status": "OK", "Nonce": "1", "Message": "this is a string"`))
			}

			callNum += 1
		} else {
			t.Fatal("Unknown dashboard API request", r)
		}
	}))
	defer ts.Close()

	apisMu.Lock()
	apisByID = make(map[string]*APISpec)
	apisMu.Unlock()

	globalConf := config.Global()
	globalConf.UseDBAppConfigs = true
	globalConf.AllowInsecureConfigs = true
	globalConf.DBAppConfOptions.ConnectionString = ts.URL
	config.SetGlobal(globalConf)

	defer ResetTestConfig()

	var wg sync.WaitGroup
	wg.Add(1)
	msg := redis.Message{Data: []byte(`{"Command": "ApiUpdated"}`)}
	handled := func(got NotificationCommand) {
		if want := NoticeApiUpdated; got != want {
			t.Fatalf("want %q, got %q", want, got)
		}
	}
	handleRedisEvent(msg, handled, wg.Done)

	// Since we already know that reload is queued
	ReloadTick <- time.Time{}

	// Wait for the reload to finish, then check it worked
	wg.Wait()
	apisMu.RLock()
	if len(apisByID) != 1 {
		t.Error("should return array with one spec", apisByID)
	}
	apisMu.RUnlock()

	// Second call

	var wg2 sync.WaitGroup
	wg2.Add(1)
	handleRedisEvent(msg, handled, wg2.Done)

	// Since we already know that reload is queued
	ReloadTick <- time.Time{}

	// Wait for the reload to finish, then check it worked
	wg2.Wait()
	apisMu.RLock()
	if len(apisByID) != 1 {
		t.Error("second call should return array with one spec", apisByID)
	}
	apisMu.RUnlock()
}
