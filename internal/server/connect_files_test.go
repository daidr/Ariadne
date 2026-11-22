package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/files"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
)

func testWorkbookBytes(t *testing.T, sheet string) []byte {
	t.Helper()
	var data bytes.Buffer
	book := zip.NewWriter(&data)
	for name, contents := range map[string]string{
		"[Content_Types].xml":        "<Types/>",
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Checks" r:id="r1"/><sheet name="Next" r:id="r2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="r1" Target="worksheets/sheet.xml"/><Relationship Id="r2" Target="/xl/worksheets/next.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       "<sst><si><r><t>Build</t></r><r><t> passed</t></r></si></sst>",
		"xl/worksheets/sheet.xml":    "<worksheet><sheetData>" + sheet + "</sheetData></worksheet>",
		"xl/worksheets/next.xml":     `<worksheet><sheetData><row><c t="inlineStr"><is><t>Next steps</t></is></c></row></sheetData></worksheet>`,
	} {
		entry, err := book.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := book.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func localizedErrorFromConnect(t *testing.T, err error) *ariadnev1.LocalizedError {
	t.Helper()
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("expected *connect.Error, got %T (%v)", err, err)
	}
	for _, detail := range connectErr.Details() {
		value, valueErr := detail.Value()
		if valueErr != nil {
			t.Fatalf("decode error detail: %v", valueErr)
		}
		if localized, ok := value.(*ariadnev1.LocalizedError); ok {
			return localized
		}
	}
	t.Fatalf("expected LocalizedError detail on %v", connectErr)
	return nil
}

func TestFileServiceGetFileSheet(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "files.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Sheets"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "sheet-secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	srv := httptest.NewServer(api)
	defer srv.Close()

	// Upload a workbook and a plain text file via the binary upload endpoint.
	workbookData := testWorkbookBytes(t, `<row r="3"><c r="B3" t="s"><v>0</v></c><c r="D3" t="b"><v>1</v></c></row>`)
	upload := func(contentType string, body []byte) files.Stored {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/projects/"+project.ID+"/files?for=message", bytes.NewReader(body))
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		req.Header.Set("Authorization", "Bearer sheet-secret")
		req.Header.Set("Content-Type", contentType)
		res, doErr := srv.Client().Do(req)
		if doErr != nil {
			t.Fatal(doErr)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("upload returned status %d", res.StatusCode)
		}
		var payload struct {
			File files.Stored `json:"file"`
		}
		if decErr := json.NewDecoder(res.Body).Decode(&payload); decErr != nil {
			t.Fatal(decErr)
		}
		return payload.File
	}

	xlsxFile := upload("application/octet-stream", workbookData)
	if xlsxFile.MIME != files.SpreadsheetMIME {
		t.Fatalf("expected spreadsheet MIME, got %q", xlsxFile.MIME)
	}
	textFile := upload("text/plain", []byte("not a workbook"))

	// Unauthenticated RPC fails with CodeUnauthenticated.
	unauthClient := ariadnev1connect.NewFileServiceClient(srv.Client(), srv.URL+rpcPrefix)
	if _, err := unauthClient.GetFileSheet(ctx, connect.NewRequest(&ariadnev1.GetFileSheetRequest{
		FileId: xlsxFile.ID,
		Index:  0,
	})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected CodeUnauthenticated, got %v", err)
	}

	client := ariadnev1connect.NewFileServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("sheet-secret")),
	)

	// Sheet 0 returns expected sheets, sparse row cells, and 1-based row numbers.
	first, err := client.GetFileSheet(ctx, connect.NewRequest(&ariadnev1.GetFileSheetRequest{
		FileId: xlsxFile.ID,
		Index:  0,
	}))
	if err != nil {
		t.Fatalf("GetFileSheet(0) failed: %v", err)
	}
	if first.Msg.GetTruncated() ||
		!reflect.DeepEqual(first.Msg.GetSheets(), []string{"Checks", "Next"}) ||
		!reflect.DeepEqual(first.Msg.GetRowNumbers(), []int64{3}) ||
		len(first.Msg.GetRows()) != 1 ||
		!reflect.DeepEqual(first.Msg.GetRows()[0].GetCells(), []string{"", "Build passed", "", "true"}) {
		t.Fatalf("unexpected first sheet response: %+v", first.Msg)
	}

	// Sheet 1 returns the second worksheet.
	second, err := client.GetFileSheet(ctx, connect.NewRequest(&ariadnev1.GetFileSheetRequest{
		FileId: xlsxFile.ID,
		Index:  1,
	}))
	if err != nil {
		t.Fatalf("GetFileSheet(1) failed: %v", err)
	}
	if len(second.Msg.GetRows()) != 1 ||
		!reflect.DeepEqual(second.Msg.GetRows()[0].GetCells(), []string{"Next steps"}) ||
		!reflect.DeepEqual(second.Msg.GetRowNumbers(), []int64{1}) {
		t.Fatalf("unexpected second sheet response: %+v", second.Msg)
	}

	// Negative index fails with CodeInvalidArgument + error.invalidBody.
	_, err = client.GetFileSheet(ctx, connect.NewRequest(&ariadnev1.GetFileSheetRequest{
		FileId: xlsxFile.ID,
		Index:  -1,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("negative index: expected CodeInvalidArgument, got %v", err)
	}
	if detail := localizedErrorFromConnect(t, err); detail.GetKey() != "error.invalidBody" ||
		detail.GetParams()["reason"].GetStringValue() != "invalid worksheet index" {
		t.Fatalf("unexpected negative index detail: %+v", detail)
	}

	// Out-of-bounds worksheet index fails with CodeInvalidArgument + filePreview.error.
	_, err = client.GetFileSheet(ctx, connect.NewRequest(&ariadnev1.GetFileSheetRequest{
		FileId: xlsxFile.ID,
		Index:  2,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing sheet index: expected CodeInvalidArgument, got %v", err)
	}
	if detail := localizedErrorFromConnect(t, err); detail.GetKey() != "filePreview.error" {
		t.Fatalf("unexpected missing sheet detail: %+v", detail)
	}

	// Non-spreadsheet file fails with CodeInvalidArgument + filePreview.error.
	_, err = client.GetFileSheet(ctx, connect.NewRequest(&ariadnev1.GetFileSheetRequest{
		FileId: textFile.ID,
		Index:  0,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("non-spreadsheet: expected CodeInvalidArgument, got %v", err)
	}
	if detail := localizedErrorFromConnect(t, err); detail.GetKey() != "filePreview.error" {
		t.Fatalf("unexpected non-spreadsheet detail: %+v", detail)
	}

	// Invalid ID format and nonexistent file both return CodeNotFound + error.notFound.
	for _, id := range []string{"bad-id", "fil_missing"} {
		_, err = client.GetFileSheet(ctx, connect.NewRequest(&ariadnev1.GetFileSheetRequest{
			FileId: id,
			Index:  0,
		}))
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("%s: expected CodeNotFound, got %v", id, err)
		}
		if detail := localizedErrorFromConnect(t, err); detail.GetKey() != "error.notFound" {
			t.Fatalf("%s: unexpected detail: %+v", id, detail)
		}
	}

	// Legacy JSON route is removed, while raw file download still works.
	legacyReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/files/"+xlsxFile.ID+"/sheet?index=0", nil)
	legacyReq.Header.Set("Authorization", "Bearer sheet-secret")
	legacyRes, err := srv.Client().Do(legacyReq)
	if err != nil {
		t.Fatal(err)
	}
	legacyRes.Body.Close()
	if legacyRes.StatusCode != http.StatusNotFound {
		t.Fatalf("legacy /sheet route returned %d, want 404", legacyRes.StatusCode)
	}

	rawReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+xlsxFile.URL, nil)
	rawReq.Header.Set("Authorization", "Bearer sheet-secret")
	rawRes, err := srv.Client().Do(rawReq)
	if err != nil {
		t.Fatal(err)
	}
	rawRes.Body.Close()
	if rawRes.StatusCode != http.StatusOK {
		t.Fatalf("raw file download returned %d, want 200", rawRes.StatusCode)
	}
}
