// Package mdns provides utility functions for DNS name processing and
// manipulation, supporting both service name parsing and DNS presentation
// format unescaping.
package mdns

import (
	"strings"
)

// parseSubtypes extracts the primary service type and any subtypes from
// a service specification string. The input format follows DNS-SD conventions
// where subtypes are comma-separated: "service,subtype1,subtype2".
//
// Returns the primary service type and a slice of subtype strings.
// If no subtypes are present, the subtypes slice will be empty.
func parseSubtypes(service string) (string, []string) {
	subtypes := strings.Split(service, ",")
	return subtypes[0], subtypes[1:]
}

// trimDot removes leading and trailing dots from a DNS name string.
// This is useful for normalizing domain names and preventing double dots
// when constructing fully qualified domain names (FQDNs).
//
// Example: ".local." becomes "local", "service." becomes "service"
func trimDot(s string) string {
	return strings.Trim(s, ".")
}

// dnsUnescape converts a DNS presentation-escaped string back to its
// original form by processing escape sequences as defined in RFC 1035.
//
// Supported escape sequences:
//   - "\\"    -> backslash character
//   - "\ "    -> space character
//   - "\."    -> dot character
//   - "\DDD"  -> ASCII character (3-digit octal code)
//
// This function handles the escape sequences produced by miekg/dns library
// when parsing DNS names containing special characters.
func dnsUnescape(s string) string {
	if len(s) == 0 {
		return s
	}

	var b strings.Builder
	b.Grow(len(s)) // Pre-allocate for efficiency

	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' {
			// Handle numeric escape sequence \DDD (3 digits)
			if i+3 < len(s) && isDigit(s[i+1]) && isDigit(s[i+2]) && isDigit(s[i+3]) {
				num := (int(s[i+1]-'0')*100 + int(s[i+2]-'0')*10 + int(s[i+3]-'0'))
				b.WriteByte(byte(num))
				i += 3 // Skip the 3 digits we processed
			} else if i+1 < len(s) {
				// Handle character escape sequences (\, ., space, etc.)
				b.WriteByte(s[i+1])
				i++ // Skip the escaped character
			} else {
				// Trailing backslash with no following character - ignore
				// This handles malformed input gracefully
			}
		} else {
			// Regular character, copy as-is
			b.WriteByte(c)
		}
	}
	return b.String()
}

// isDigit checks if a byte represents an ASCII digit character (0-9).
// This is used for validating numeric escape sequences in DNS names.
func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}
