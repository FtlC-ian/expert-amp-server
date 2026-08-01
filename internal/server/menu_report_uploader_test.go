package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FtlC-ian/expert-amp-server/internal/menudebug"
)

func TestMenuReportHTTPUploaderPostsOnlyTheReport(t *testing.T) {
	var received menudebug.Report
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request = %s content-type=%q", r.Method, r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer collector.Close()

	uploader, err := NewMenuReportHTTPUploader(collector.URL+"/v1/menu-reports", collector.Client())
	if err != nil {
		t.Fatal(err)
	}
	want := menudebug.Report{SchemaVersion: menudebug.ReportSchemaVersion, Model: "EXPERT 1.3K-FA", Firmware: "unknown", ServerVersion: "v0.4.0"}
	if err := uploader.Upload(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if received.Model != want.Model || received.SchemaVersion != want.SchemaVersion {
		t.Fatalf("received = %+v", received)
	}
}

func TestMenuReportHTTPUploaderRequiresHTTPSOutsideLoopback(t *testing.T) {
	if _, err := NewMenuReportHTTPUploader("http://collector.example/v1/menu-reports", nil); err == nil {
		t.Fatal("insecure public collector URL was accepted")
	}
	if _, err := NewMenuReportHTTPUploader("https://user:pass@collector.example/v1/menu-reports", nil); err == nil {
		t.Fatal("credential-bearing collector URL was accepted")
	}
}

func TestMenuReportHTTPUploaderRejectsCollectorFailure(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusTooManyRequests)
	}))
	defer collector.Close()
	uploader, err := NewMenuReportHTTPUploader(collector.URL, collector.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := uploader.Upload(context.Background(), menudebug.Report{}); err == nil {
		t.Fatal("collector failure was accepted")
	}
}

func TestMenuReportHTTPUploaderRejectsRedirects(t *testing.T) {
	reachedRedirectTarget := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reachedRedirectTarget = true
	}))
	defer target.Close()

	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer collector.Close()

	uploader, err := NewMenuReportHTTPUploader(collector.URL, collector.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := uploader.Upload(context.Background(), menudebug.Report{}); err == nil {
		t.Fatal("collector redirect was accepted")
	}
	if reachedRedirectTarget {
		t.Fatal("report body was sent to redirect target")
	}
}
