package s3client

import "testing"

func TestConfigNormalize_AddsHTTPSWhenMissing(t *testing.T) {
	c := Config{Endpoint: "fsn1.your-objectstorage.com", Region: "eu-central",
		AccessKey: "ak", SecretKey: "sk"}
	c.Normalize()
	if c.Endpoint != "https://fsn1.your-objectstorage.com" {
		t.Errorf("got %q, want https://fsn1.your-objectstorage.com", c.Endpoint)
	}
}

func TestConfigNormalize_KeepsExplicitScheme(t *testing.T) {
	for _, in := range []string{
		"https://minio.lab.example",
		"http://localhost:9000",
		"https://s3.eu-central-1.amazonaws.com",
	} {
		c := Config{Endpoint: in, Region: "x", AccessKey: "a", SecretKey: "s"}
		c.Normalize()
		if c.Endpoint != in {
			t.Errorf("schemed input %q normalised to %q (should be unchanged)", in, c.Endpoint)
		}
	}
}

func TestConfigNormalize_StripsTrailingSlash(t *testing.T) {
	c := Config{Endpoint: "https://minio.lab.example/", Region: "x",
		AccessKey: "a", SecretKey: "s"}
	c.Normalize()
	if c.Endpoint != "https://minio.lab.example" {
		t.Errorf("trailing slash not stripped: %q", c.Endpoint)
	}
}

func TestConfigNormalize_TrimsWhitespace(t *testing.T) {
	c := Config{
		Endpoint:  "   https://minio.lab.example   ",
		Region:    "  eu-central  ",
		AccessKey: " ak ",
		SecretKey: " sk ",
	}
	c.Normalize()
	if c.Endpoint != "https://minio.lab.example" || c.Region != "eu-central" ||
		c.AccessKey != "ak" || c.SecretKey != "sk" {
		t.Errorf("whitespace not trimmed: %+v", c)
	}
}

func TestConfigValidate_NamesMissingFields(t *testing.T) {
	c := Config{Endpoint: "https://x", Region: "y"} // no keys
	err := c.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"access key", "secret key"} {
		if !contains(err.Error(), want) {
			t.Errorf("error should name missing field %q; got %v", want, err)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
