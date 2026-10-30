package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	guidPattern       = regexp.MustCompile(`^\{?[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\}?$`)
	identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
	columnTypePattern = regexp.MustCompile(`^([sSlL])(\d+)$|^([iI])([24])$|^([vV])0$`)
)

// NilGUID is the all-zero GUID in the form NormalizeGUID returns.
const NilGUID = "{00000000-0000-0000-0000-000000000000}"

// NormalizeGUID returns s braced and upper case, or an error if s is not a GUID.
func NormalizeGUID(s string) (string, error) {
	if !guidPattern.MatchString(s) || strings.HasPrefix(s, "{") != strings.HasSuffix(s, "}") {
		return "", fmt.Errorf("%q is not a GUID", s)
	}

	return "{" + strings.ToUpper(strings.Trim(s, "{}")) + "}", nil
}

// Version is a ProductVersion: three fields and an optional fourth.
type Version struct {
	Major, Minor, Build, Revision int
	Fields                        int
}

// ParseVersion parses major.minor.build[.revision] within the limits of
// ProductVersion (255.255.65535, revision 65535).
func ParseVersion(text string) (Version, error) {
	parts := strings.Split(text, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return Version{}, fmt.Errorf("version %q must have two to four fields", text)
	}

	limits := []int{255, 255, 65535, 65535}
	version := Version{Fields: len(parts)}
	dst := []*int{&version.Major, &version.Minor, &version.Build, &version.Revision}

	for i, p := range parts {
		// Digits only: Atoi also takes a sign, and "-0" would pass as 0.
		num, err := strconv.Atoi(p)
		if err != nil || strings.Trim(p, "0123456789") != "" {
			return Version{}, fmt.Errorf("version %q: field %d is not a number", text, i+1)
		}

		if num > limits[i] {
			return Version{}, fmt.Errorf("version %q: field %d exceeds %d", text, i+1, limits[i])
		}

		*dst[i] = num
	}

	return version, nil
}

// PageCount converts installerVersion ("5.0", "4.5") into the Summary
// Information Page Count (major × 100 + minor). A major alone ("5") is
// major.0: the schema allows installerVersion as a number, and as a number
// 5 and 5.0 are the same.
func PageCount(installerVersion string) (int, error) {
	major, minor, ok := strings.Cut(installerVersion, ".")
	if !ok {
		minor = "0"
	}

	majorNum, err1 := strconv.Atoi(major)
	minorNum, err2 := strconv.Atoi(minor)

	// Digits only, as in ParseVersion: Atoi also takes a sign, and "+5" or
	// "-0" would pass. The major limit keeps major × 100 from wrapping around.
	if err1 != nil || err2 != nil || strings.Trim(major, "0123456789") != "" ||
		strings.Trim(minor, "0123456789") != "" || majorNum > 99 || minorNum > 99 {
		return 0, fmt.Errorf("installer version %q must be major.minor", installerVersion)
	}

	return majorNum*100 + minorNum, nil
}

// MinPageCount is the lowest Page Count msi-builder writes for a platform. x64
// installed from 150 up in SUM-03, but 200 is kept until ICE80 has been run.
func MinPageCount(p Platform) int {
	switch p {
	case PlatformX64:
		return 200
	case PlatformArm64:
		return 500
	case PlatformX86:
	}

	return 100
}

// MaxPageCount is the highest Page Count Windows Installer 5.0 accepts
// (SUM-03: 501 and above fail with 1613 on every platform).
const MaxPageCount = 500

// IsPublicProperty reports whether name has no lower-case letter.
func IsPublicProperty(name string) bool {
	return strings.ToUpper(name) == name
}

// IsIdentifier reports whether s is a valid Windows Installer identifier.
func IsIdentifier(s string) bool {
	return identifierPattern.MatchString(s)
}

// ansiCodepages may be stored in the Summary Information stream (SUM-04).
var ansiCodepages = map[int]bool{ //nolint:gochecknoglobals // lookup table
	874: true, 932: true, 936: true, 949: true, 950: true,
	1250: true, 1251: true, 1252: true, 1253: true, 1254: true,
	1255: true, 1256: true, 1257: true, 1258: true,
}

// SummaryCodepage returns the code page written to the Summary Information
// stream for a database code page: the same page when it is ANSI, else 1252.
// Summary code page 65001 or 1200 makes msiexec fail with 1620 (SUM-04).
func SummaryCodepage(databaseCodepage int) int {
	if ansiCodepages[databaseCodepage] {
		return databaseCodepage
	}

	return 1252
}

const invalidLongNameChars = `\/:*?"<>|`

// validLongName checks a long file or directory name.
func validLongName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, invalidLongNameChars) &&
		!strings.ContainsFunc(s, func(r rune) bool { return r < ' ' }) &&
		strings.TrimRight(s, ". ") == s && !reservedDeviceName(s)
}

// reservedDeviceName reports names that Windows maps to a device, such as
// CON, nul.txt or COM¹, which cannot be created as files or folders.
func reservedDeviceName(s string) bool {
	base, _, _ := strings.Cut(s, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))

	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}

	prefix, digit := base[:min(3, len(base))], []rune(base[min(3, len(base)):])
	if (prefix != "COM" && prefix != "LPT") || len(digit) != 1 {
		return false
	}

	return digit[0] >= '1' && digit[0] <= '9' || digit[0] == '¹' || digit[0] == '²' || digit[0] == '³'
}

// isShortName checks an 8.3 name (specification section 11.3).
func isShortName(name string) bool {
	if strings.ContainsAny(name, `\/:*?"<>|+,;=[] `) {
		return false
	}

	base, ext, hasExt := strings.Cut(name, ".")
	if hasExt && (ext == "" || len(ext) > 3 || strings.Contains(ext, ".")) {
		return false
	}

	for _, r := range name {
		if r < 0x21 || r > 0x7E {
			return false
		}
	}

	return base != "" && len(base) <= 8
}

// nameProblem explains why s cannot be a file or folder name, kind, or
// returns "". A "short|long" pair gets its own message: msi-builder generates
// every 8.3 short name (specification section 11.3), so a name is the long
// name only.
func nameProblem(s, kind string) string {
	switch {
	case strings.Contains(s, "|"):
		return fmt.Sprintf("%q has a short name part; short names are generated, so give only the long name", s)
	case !validLongName(s):
		return fmt.Sprintf("%q is not a valid %s", s, kind)
	default:
		return ""
	}
}
