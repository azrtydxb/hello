package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/phonebook"
)

func jsonReq(body string) (*httptest.ResponseRecorder, *http.Request) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return httptest.NewRecorder(), r
}

// TestAnnouncementJSONBody fails if the JSON form of an announcement upload
// does not yield the name and decoded audio, or accepts unknown fields,
// audio over 700 KiB, or a body over 1 MiB.
func TestAnnouncementJSONBody(t *testing.T) {
	audio := []byte("RIFFxxxxWAVE")
	body, _ := json.Marshal(map[string]string{"name": " hold ", "audioBase64": base64.StdEncoding.EncodeToString(audio)})
	w, r := jsonReq(string(body))
	var name string
	var data []byte
	if !readAnnouncementBody(w, r, &name, &data, "x") || name != "hold" || !bytes.Equal(data, audio) {
		t.Fatalf("got %v name %q data %q", w.Code, name, data)
	}

	for label, b := range map[string]string{
		"unknown field": `{"name":"a","audioBase64":"","file":"x"}`,
		"not base64":    `{"name":"a","audioBase64":"!!"}`,
		"too much":      `{"name":"a","audioBase64":"` + base64.StdEncoding.EncodeToString(make([]byte, maxAnnouncementJSONAudio+1)) + `"}`,
		"over 1 MiB":    `{"name":"a","audioBase64":"` + strings.Repeat("A", 1<<20) + `"}`,
	} {
		w, r := jsonReq(b)
		if readAnnouncementBody(w, r, &name, &data, "x") || w.Code != http.StatusBadRequest && w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: accepted or answered %d", label, w.Code)
		}
	}

	// Other content types still get the multipart advice.
	r = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("x"))
	r.Header.Set("Content-Type", "text/plain")
	w = httptest.NewRecorder()
	if readAnnouncementBody(w, r, &name, &data, "send it right") || !strings.Contains(w.Body.String(), "send it right") {
		t.Errorf("text/plain: %d %s", w.Code, w.Body)
	}
}

// TestPhoneImportJSONRecords fails if JSON rows do not become the same
// records the CSV form yields, with the blf list joined for the shared check.
func TestPhoneImportJSONRecords(t *testing.T) {
	w, r := jsonReq(`{"rows":[{"mac":"aa:bb:cc:00:00:01","vendor":"yealink","model":"T54W","extension":"101","label":"Desk","blf":["102","103"]},{"mac":"x","vendor":"v","model":"m","extension":"1"}]}`)
	recs, ok := phoneImportRecords(w, r)
	if !ok || len(recs) != 2 || recs[0].line != 1 || recs[1].line != 2 {
		t.Fatalf("recs %v ok %v %s", recs, ok, w.Body)
	}
	if got := strings.Join(recs[0].fields, ","); got != "aa:bb:cc:00:00:01,yealink,T54W,101,Desk,102;103" {
		t.Errorf("fields %s", got)
	}
	csvR := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("mac,vendor,model,extension\naa,y,T,1\n"))
	csvR.Header.Set("Content-Type", "text/csv")
	if recs, ok := phoneImportRecords(httptest.NewRecorder(), csvR); !ok || len(recs) != 1 {
		t.Errorf("csv form: %v %v", recs, ok)
	}
	for label, b := range map[string]string{
		"no rows":       `{"rows":[]}`,
		"unknown field": `{"rows":[{"mac":"a","bogus":1}]}`,
		"not json":      `[`,
	} {
		w, r := jsonReq(b)
		if _, ok := phoneImportRecords(w, r); ok || w.Code != http.StatusBadRequest {
			t.Errorf("%s: ok %v code %d", label, ok, w.Code)
		}
	}
}

// TestPhonebookImportJSONSource fails if JSON rows are not rendered as the
// CSV that phonebook.ParseCSV reads (so every CSV rule applies to them), or
// an unknown column is accepted.
func TestPhonebookImportJSONSource(t *testing.T) {
	w, r := jsonReq(`{"rows":[{"first_name":"Ann, B","last_name":"Lee","work":"+3212345678","note":"say \"hi\""},{"last_name":"Only"}]}`)
	src, ok := importSource(w, r)
	if !ok {
		t.Fatal(w.Body)
	}
	b, _ := io.ReadAll(src)
	want := strings.Join(phonebook.CSVColumns, ",") + "\n" +
		"\"Ann, B\",Lee,,+3212345678,,,,\"say \"\"hi\"\"\"\n" +
		",Only,,,,,,\n"
	if string(b) != want {
		t.Errorf("csv = %q, want %q", b, want)
	}
	for label, body := range map[string]string{
		"no rows":        `{"rows":[]}`,
		"unknown column": `{"rows":[{"nickname":"x"}]}`,
		"not json":       `{`,
	} {
		w, r := jsonReq(body)
		if _, ok := importSource(w, r); ok || w.Code != http.StatusBadRequest {
			t.Errorf("%s: ok %v code %d", label, ok, w.Code)
		}
	}
}
