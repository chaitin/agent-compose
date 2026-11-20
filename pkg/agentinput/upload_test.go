package agentinput

import (
	"errors"
	"strings"
	"testing"
)

func uploadLimits() Limits {
	limits := DefaultLimits()
	limits.MaxUploadFileBytes = 100
	limits.MaxUploadTurnBytes = 150
	return limits
}

func uploadPart(id string, size int64) Part {
	return FilePart(File{Name: id + ".bin", Source: SourceUpload, UploadID: id, UploadPath: "/staged/" + id, UploadSize: size})
}

func TestValidateUploadSource(t *testing.T) {
	text := TextPart("read these")
	cases := map[string]struct {
		parts  []Part
		limits Limits
		want   string
	}{
		"missing id":      {[]Part{text, FilePart(File{Name: "a", Source: SourceUpload})}, uploadLimits(), "upload_id is required"},
		"unavailable":     {[]Part{text, uploadPart("a", 10)}, DefaultLimits(), "unavailable"},
		"unresolved":      {[]Part{text, FilePart(File{Name: "a", Source: SourceUpload, UploadID: "a"})}, uploadLimits(), "not resolved"},
		"file over limit": {[]Part{text, uploadPart("a", 101)}, uploadLimits(), "upload limit is 100"},
		"turn over limit": {[]Part{text, uploadPart("a", 100), uploadPart("b", 100)}, uploadLimits(), "turn upload files are 200 bytes"},
	}
	for name, test := range cases {
		err := Validate(test.parts, test.limits)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: err = %v, want %q", name, err, test.want)
		}
	}
}

func TestValidateCountsUploadsSeparatelyFromInlineBytes(t *testing.T) {
	limits := uploadLimits()
	limits.MaxFileBytes = 10
	limits.MaxTurnBytes = 10
	parts := []Part{
		TextPart("both"),
		FilePart(File{Name: "small.txt", Source: SourceInline, Data: []byte("0123456789")}),
		uploadPart("large", 100),
	}
	if err := Validate(parts, limits); err != nil {
		t.Fatalf("Validate = %v, want the upload outside the inline budget", err)
	}
}
