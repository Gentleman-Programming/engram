// Package scrub provides deterministic, fail-closed detection of sensitive data
// (cardholder data, secrets, PII, internal identifiers) at the cloud ingestion
// boundary.
//
// Scan reports findings WITHOUT the matched values, so callers can reject or
// audit a mutation batch without ever logging the sensitive content itself.
//
// # Design and limitations (READ THIS before trusting it)
//
// These detectors are deterministic (normalization + regex + Luhn + Shannon
// entropy), not an ML classifier. They are a defense-in-depth layer to stop
// ACCIDENTAL leakage — they are NOT a PCI compliance boundary on their own.
// A regex/entropy scrubber will always have evasions. Known limitations:
//
//   - CVV/CVC has no intrinsic shape (any 3–4 digits). Only the CONTEXTUAL form
//     ("cvv: 123") is detectable; a bare standalone CVV cannot be caught.
//   - High-entropy detection has false positives (hashes, long IDs) and, in
//     fail-closed strict mode, that is intentional — it rejects the save.
//   - Data that is encoded/compressed/encrypted BEFORE reaching Scan is opaque.
//     The cloud ingest path passes plaintext JSON, so that holds there; do not
//     reuse Scan on already-encoded payloads.
//
// The only real guarantee for true cardholder data is never writing it into the
// system at all. Scan reduces accidental exposure; it does not replace that.
package scrub

import (
	"math"
	"os"
	"regexp"
	"strings"
)

// Mode controls enforcement. It is read from ENGRAM_SCRUB and defaults to Off
// so the feature is opt-in and never changes behavior for existing deployments.
type Mode int

const (
	// ModeOff disables scanning entirely (default).
	ModeOff Mode = iota
	// ModeWarn scans and reports findings but does not block the write.
	ModeWarn
	// ModeStrict blocks the write on HIGH-severity findings; heuristic findings
	// are logged but do not block (preserves usability for normal dev notes).
	ModeStrict
	// ModeParanoid blocks the write on ANY finding, including heuristic ones.
	ModeParanoid
)

// ModeFromEnv resolves the enforcement mode from ENGRAM_SCRUB.
// Accepted: "" / "off" → Off, "warn" → Warn, "strict" → Strict, "paranoid" → Paranoid.
func ModeFromEnv() Mode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ENGRAM_SCRUB"))) {
	case "warn":
		return ModeWarn
	case "strict":
		return ModeStrict
	case "paranoid":
		return ModeParanoid
	default:
		return ModeOff
	}
}

// Severity tiers a finding by detection confidence so callers can block on
// high-confidence hits without being swamped by heuristic false positives.
type Severity int

const (
	// SeverityHeuristic is FP-prone (entropy, long hex, bare IDs, phone, RFC).
	SeverityHeuristic Severity = iota
	// SeverityHigh is high-confidence (Luhn PAN, keys, tokens, conn strings, email).
	SeverityHigh
)

// Blocks reports whether a finding should block a write under the given mode.
func (f Finding) Blocks(m Mode) bool {
	switch m {
	case ModeParanoid:
		return true
	case ModeStrict:
		return f.Severity == SeverityHigh
	default:
		return false
	}
}

// Category classifies a finding without exposing the matched value.
type Category string

const (
	CategoryPAN    Category = "cardholder_data" // PAN (Luhn-validated)
	CategoryCVV    Category = "cardholder_data" // contextual CVV/CVC
	CategorySecret Category = "secret"          // keys, tokens, private keys, conn strings
	CategoryPII    Category = "pii"             // email, RFC, CURP, phone
	CategoryID     Category = "internal_id"     // live production identifiers
)

// Finding describes a single detection. It carries NO matched value — only the
// category, detector name, and severity — so it is safe to log and audit.
type Finding struct {
	Category Category
	Detector string
	Severity Severity
}

var (
	// PAN candidate: a digit followed by 12+ more digits, allowing common
	// separators (space, dash, dot, NBSP) BUT NOT requiring word boundaries —
	// so "ref4111..." and "4111.1111..." are still found. Luhn (below) and a
	// sliding window over long runs remove false positives / catch padding.
	rePANCandidate = regexp.MustCompile(`[0-9](?:[ \-.\x{00A0}]?[0-9]){12,}`)

	rePrivateKey = regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----`)
	reJWT        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	reAWSKey     = regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)
	// Well-known provider token prefixes (Stripe, GitHub, Slack, OpenAI, Google).
	// Organization-specific prefixes belong in custom patterns (see config.go).
	reTokenPrefix = regexp.MustCompile(`\b(?:sk_live_|rk_live_|sk-|xox[baprs]-|ghp_|gho_|github_pat_|AIza)[A-Za-z0-9_\-]{10,}\b`)
	// Track 2 data: {PAN}={YYYY}{MM} — ISO card standard, highly distinctive.
	reTrack2 = regexp.MustCompile(`\b\d{15,16}=\d{4}\b`)
	// CLABE — Mexican 18-digit interbank account number (financial PII).
	reCLABE = regexp.MustCompile(`\b\d{18}\b`)
	// Generic cardholder field names paired with a value (e.g. "cvc": 123).
	// Deployment-specific field names belong in custom patterns (see config.go).
	reSensitiveField = regexp.MustCompile(`(?i)\b(?:card_?number|cardholder|pan|cvc|cvv2?)\b\s*["']?\s*[:=]\s*["']?\S`)
	// scheme://user:password@host  (postgres, mysql, mongodb, redis, amqp, http…)
	reConnString = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://[^\s:/@]+:[^\s:/@]+@`)
	reBearer     = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._\-]{12,}`)
	// key-value secret assignment: password / api_key / token / secret = <value>
	reKVSecret = regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|api[_-]?key|apikey|access[_-]?token|auth[_-]?token|client[_-]?secret|private[_-]?key|token)\b["']?\s*[:=]\s*["']?[^\s"',}]{6,}`)
	// Candidate high-entropy tokens (base64/base64url-ish) and long hex blobs.
	reEntropyTok = regexp.MustCompile(`[A-Za-z0-9+/=_\-]{20,}`)
	reLongHex    = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)

	reEmail = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	// Mexican RFC (12/13) and CURP (18), case-insensitive (agents may lowercase).
	reRFC  = regexp.MustCompile(`(?i)\b[A-ZÑ&]{3,4}\d{6}[A-Z0-9]{3}\b`)
	reCURP = regexp.MustCompile(`(?i)\b[A-Z]{4}\d{6}[HM][A-Z]{5}[0-9A-Z]\d\b`)
	// Mexican phone: +52 prefixed, or a separator-grouped 10-digit number.
	rePhoneMX = regexp.MustCompile(`(?:\+?52[ \-.]?)?(?:\(?\d{2,3}\)?[ \-.])\d{3,4}[ \-.]?\d{4}\b`)
	// Contextual CVV/CVC: the keyword adjacent to 3–4 digits.
	reCVVCtx = regexp.MustCompile(`(?i)\b(?:cvv2?|cvc2?|cvn|cid|security\s*code|c[oó]digo\s*de\s*seguridad)\b\W{0,4}\d{3,4}\b`)
	// Mongo ObjectId — 24-char hex, a common record-identifier shape.
	reObjectID = regexp.MustCompile(`\b[0-9a-f]{24}\b`)
)

// Scan inspects raw bytes (typically a mutation payload) and returns every
// distinct finding. An empty slice means no sensitive data was detected.
// Scan never returns the matched substring.
func Scan(b []byte) []Finding {
	s := normalize(string(b))
	var out []Finding

	// High-confidence detectors — block in strict. Near-zero false positives.
	if detectPAN(s) {
		out = append(out, Finding{CategoryPAN, "luhn_pan", SeverityHigh})
	}
	if reTrack2.MatchString(s) {
		out = append(out, Finding{CategoryPAN, "track2", SeverityHigh})
	}
	if reCVVCtx.MatchString(s) {
		out = append(out, Finding{CategoryCVV, "cvv_contextual", SeverityHigh})
	}
	if reSensitiveField.MatchString(s) { // generic cardholder field names
		out = append(out, Finding{CategoryPAN, "sensitive_field", SeverityHigh})
	}
	for _, d := range []struct {
		re   *regexp.Regexp
		name string
	}{
		{rePrivateKey, "private_key"},
		{reJWT, "jwt"},
		{reAWSKey, "aws_access_key"},
		{reTokenPrefix, "token_prefix"},
		{reConnString, "connection_string"},
		{reBearer, "bearer_token"},
		{reKVSecret, "kv_secret"},
	} {
		if d.re.MatchString(s) {
			out = append(out, Finding{CategorySecret, d.name, SeverityHigh})
		}
	}
	if reEmail.MatchString(s) {
		out = append(out, Finding{CategoryPII, "email", SeverityHigh})
	}
	if reCURP.MatchString(s) { // 18-char structured — low FP
		out = append(out, Finding{CategoryPII, "curp", SeverityHigh})
	}

	// Heuristic detectors — FP-prone (git SHAs, hashes, UUIDs, slugs match
	// entropy/hex; random tokens match RFC/ObjectId). Logged in strict, block
	// only in paranoid.
	if detectHighEntropy(s) {
		out = append(out, Finding{CategorySecret, "high_entropy", SeverityHeuristic})
	}
	if reLongHex.MatchString(s) {
		out = append(out, Finding{CategorySecret, "long_hex", SeverityHeuristic})
	}
	if reRFC.MatchString(s) {
		out = append(out, Finding{CategoryPII, "rfc", SeverityHeuristic})
	}
	if rePhoneMX.MatchString(s) {
		out = append(out, Finding{CategoryPII, "phone_mx", SeverityHeuristic})
	}
	if reCLABE.MatchString(s) {
		out = append(out, Finding{CategoryPII, "clabe", SeverityHeuristic})
	}
	if reObjectID.MatchString(s) {
		out = append(out, Finding{CategoryID, "mongo_objectid", SeverityHeuristic})
	}

	// Deployment-specific detectors loaded from config (kept out of this OSS code).
	out = append(out, customFindings(s)...)

	return out
}

// normalize folds full-width ASCII digits (U+FF10–U+FF19) to their ASCII form so
// detectors cannot be evaded with Unicode digit homoglyphs.
func normalize(s string) string {
	if !strings.ContainsRune(s, '０') && !containsFullwidthDigit(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= '０' && r <= '９' {
			b.WriteRune('0' + (r - '０'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func containsFullwidthDigit(s string) bool {
	for _, r := range s {
		if r >= '０' && r <= '９' {
			return true
		}
	}
	return false
}

// detectPAN strips separators from every candidate run and Luhn-checks it.
// For runs longer than a PAN it slides a 13–19 digit window, so a PAN padded
// with extra digits ("0000"+PAN) is still caught.
func detectPAN(s string) bool {
	for _, cand := range rePANCandidate.FindAllString(s, -1) {
		d := stripNonDigits(cand)
		n := len(d)
		switch {
		case n >= 13 && n <= 19:
			if luhnValid(d) {
				return true
			}
		case n > 19 && n <= 128: // bounded sliding window
			for start := 0; start+13 <= n; start++ {
				for l := 13; l <= 19 && start+l <= n; l++ {
					if luhnValid(d[start : start+l]) {
						return true
					}
				}
			}
		}
	}
	return false
}

func stripNonDigits(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteByte(byte(r))
		}
	}
	return b.String()
}

// luhnValid runs the Luhn checksum over a pure-digit string of PAN length.
func luhnValid(d string) bool {
	if len(d) < 13 || len(d) > 19 {
		return false
	}
	sum := 0
	double := false
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if double {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		double = !double
	}
	return sum%10 == 0
}

// detectHighEntropy flags base64/base64url-ish tokens of length >= 20 whose
// Shannon entropy per character is high enough to look like a random secret.
// Threshold ~3.5 bits/char clears English prose and repeated padding while
// catching random keys; in strict mode any false positive simply blocks the save.
func detectHighEntropy(s string) bool {
	for _, tok := range reEntropyTok.FindAllString(s, -1) {
		if shannonBits(tok) >= 3.5 {
			return true
		}
	}
	return false
}

func shannonBits(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]float64
	n := 0.0
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
		n++
	}
	h := 0.0
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}
