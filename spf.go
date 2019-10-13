// Package spf implements SPF (Sender Policy Framework) lookup and validation.
//
// Sender Policy Framework (SPF) is a simple email-validation system designed
// to detect email spoofing by providing a mechanism to allow receiving mail
// exchangers to check that incoming mail from a domain comes from a host
// authorized by that domain's administrators [Wikipedia].
//
// This is a Go implementation of it, which is used by the chasquid SMTP
// server (https://blitiri.com.ar/p/chasquid/).
//
// Supported mechanisms and modifiers:
//   all
//   include
//   a
//   mx
//   ip4
//   ip6
//   redirect
//   exp (ignored)
//
// Not supported (return Neutral if used):
//   exists
//   Macros
//
// This is intentional and there are no plans to add them for now, as they are
// very rare, convoluted and not worth the additional complexity.
//
// References:
//   https://tools.ietf.org/html/rfc7208
//   https://en.wikipedia.org/wiki/Sender_Policy_Framework
package spf // import "blitiri.com.ar/go/spf"

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Functions that we can override for testing purposes.
var (
	lookupTXT  = net.LookupTXT
	lookupMX   = net.LookupMX
	lookupIP   = net.LookupIP
	lookupAddr = net.LookupAddr
	trace      = func(f string, a ...interface{}) {}
)

// The Result of an SPF check. Note the values have meaning, we use them in
// headers.  https://tools.ietf.org/html/rfc7208#section-8
type Result string

// Valid results.
var (
	// https://tools.ietf.org/html/rfc7208#section-8.1
	// Not able to reach any conclusion.
	None = Result("none")

	// https://tools.ietf.org/html/rfc7208#section-8.2
	// No definite assertion (positive or negative).
	Neutral = Result("neutral")

	// https://tools.ietf.org/html/rfc7208#section-8.3
	// Client is authorized to inject mail.
	Pass = Result("pass")

	// https://tools.ietf.org/html/rfc7208#section-8.4
	// Client is *not* authorized to use the domain
	Fail = Result("fail")

	// https://tools.ietf.org/html/rfc7208#section-8.5
	// Not authorized, but unwilling to make a strong policy statement/
	SoftFail = Result("softfail")

	// https://tools.ietf.org/html/rfc7208#section-8.6
	// Transient error while performing the check.
	TempError = Result("temperror")

	// https://tools.ietf.org/html/rfc7208#section-8.7
	// Records could not be correctly interpreted.
	PermError = Result("permerror")
)

var qualToResult = map[byte]Result{
	'+': Pass,
	'-': Fail,
	'~': SoftFail,
	'?': Neutral,
}

var (
	errLookupLimitReached = fmt.Errorf("lookup limit reached")
	errMacrosNotSupported = fmt.Errorf("macros not supported")
	errExistsNotSupported = fmt.Errorf("'exists' not supported")
	errUnknownField       = fmt.Errorf("unknown field")
	errInvalidIP          = fmt.Errorf("invalid ipX value")
	errInvalidMask        = fmt.Errorf("invalid mask")
	errNoResult           = fmt.Errorf("lookup yielded no result")

	errMatchedAll = fmt.Errorf("matched 'all'")
	errMatchedA   = fmt.Errorf("matched 'a'")
	errMatchedIP  = fmt.Errorf("matched 'ip'")
	errMatchedMX  = fmt.Errorf("matched 'mx'")
	errMatchedPTR = fmt.Errorf("matched 'ptr'")
)

// CheckHost fetches SPF records for `domain`, parses them, and evaluates them
// to determine if `ip` is permitted to send mail for it.
// Reference: https://tools.ietf.org/html/rfc7208#section-4
func CheckHost(ip net.IP, domain string) (Result, error) {
	trace("check host %q %q", ip, domain)
	r := &resolution{ip, 0, "", nil}
	return r.Check(domain)
}

// CheckHostWithSender fetches SPF records for `domain`, parses them, and
// evaluates them to determine if `ip` is permitted to send mail for it.
// The sender is used in macro expansion.
// Reference: https://tools.ietf.org/html/rfc7208#section-4
func CheckHostWithSender(ip net.IP, helo, sender string) (Result, error) {
	_, domain := split(sender)
	if domain == "" {
		domain = helo
	}

	trace("check host with sender %q %q %q (%q)", ip, helo, sender, domain)
	r := &resolution{ip, 0, sender, nil}
	return r.Check(domain)
}

// split an user@domain address into user and domain.
func split(addr string) (string, string) {
	ps := strings.SplitN(addr, "@", 2)
	if len(ps) != 2 {
		return addr, ""
	}

	return ps[0], ps[1]
}

type resolution struct {
	ip    net.IP
	count uint

	sender string

	// Result of doing a reverse lookup for ip (so we only do it once).
	ipNames []string
}

func (r *resolution) Check(domain string) (Result, error) {
	r.count++
	trace("check %s %d", domain, r.count)
	txt, err := getDNSRecord(domain)
	if err != nil {
		if isTemporary(err) {
			trace("dns temp error: %v", err)
			return TempError, err
		}
		// Could not resolve the name, it may be missing the record.
		// https://tools.ietf.org/html/rfc7208#section-2.6.1
		trace("dns perm error: %v", err)
		return None, err
	}

	if txt == "" {
		// No record => None.
		// https://tools.ietf.org/html/rfc7208#section-4.6
		trace("no txt record")
		return None, nil
	}

	fields := strings.Fields(txt)

	// redirects must be handled after the rest; instead of having two loops,
	// we just move them to the end.
	var newfields, redirects []string
	for _, field := range fields {
		if strings.HasPrefix(field, "redirect=") {
			redirects = append(redirects, field)
		} else {
			newfields = append(newfields, field)
		}
	}
	fields = append(newfields, redirects...)

	for _, field := range fields {
		if strings.HasPrefix(field, "v=") {
			continue
		}

		// Limit the number of resolutions to 10
		// https://tools.ietf.org/html/rfc7208#section-4.6.4
		if r.count > 10 {
			trace("lookup limit reached")
			return PermError, errLookupLimitReached
		}

		if strings.Contains(field, "%") {
			return Neutral, errMacrosNotSupported
		}

		// See if we have a qualifier, defaulting to + (pass).
		// https://tools.ietf.org/html/rfc7208#section-4.6.2
		result, ok := qualToResult[field[0]]
		if ok {
			field = field[1:]
		} else {
			result = Pass
		}

		if field == "all" {
			// https://tools.ietf.org/html/rfc7208#section-5.1
			trace("%v matched all", result)
			return result, errMatchedAll
		} else if strings.HasPrefix(field, "include:") {
			if ok, res, err := r.includeField(result, field); ok {
				trace("include ok, %v %v", res, err)
				return res, err
			}
		} else if strings.HasPrefix(field, "a") {
			if ok, res, err := r.aField(result, field, domain); ok {
				trace("a ok, %v %v", res, err)
				return res, err
			}
		} else if strings.HasPrefix(field, "mx") {
			if ok, res, err := r.mxField(result, field, domain); ok {
				trace("mx ok, %v %v", res, err)
				return res, err
			}
		} else if strings.HasPrefix(field, "ip4:") || strings.HasPrefix(field, "ip6:") {
			if ok, res, err := r.ipField(result, field); ok {
				trace("ip ok, %v %v", res, err)
				return res, err
			}
		} else if strings.HasPrefix(field, "ptr") {
			if ok, res, err := r.ptrField(result, field, domain); ok {
				trace("ptr ok, %v %v", res, err)
				return res, err
			}
		} else if strings.HasPrefix(field, "exists") {
			trace("exists, neutral / not supported")
			return Neutral, errExistsNotSupported
		} else if strings.HasPrefix(field, "exp=") {
			trace("exp= not used, skipping")
			continue
		} else if strings.HasPrefix(field, "redirect=") {
			trace("redirect, %q", field)
			// https://tools.ietf.org/html/rfc7208#section-6.1
			result, err := r.Check(field[len("redirect="):])
			if result == None {
				result = PermError
			}
			return result, err
		} else {
			// http://www.openspf.org/SPF_Record_Syntax
			trace("permerror, unknown field")
			return PermError, errUnknownField
		}
	}

	// Got to the end of the evaluation without a result => Neutral.
	// https://tools.ietf.org/html/rfc7208#section-4.7
	trace("fallback to neutral")
	return Neutral, nil
}

// getDNSRecord gets TXT records from the given domain, and returns the SPF
// (if any).  Note that at most one SPF is allowed per a given domain:
// https://tools.ietf.org/html/rfc7208#section-3
// https://tools.ietf.org/html/rfc7208#section-3.2
// https://tools.ietf.org/html/rfc7208#section-4.5
func getDNSRecord(domain string) (string, error) {
	txts, err := lookupTXT(domain)
	if err != nil {
		return "", err
	}

	for _, txt := range txts {
		if strings.HasPrefix(txt, "v=spf1 ") {
			return txt, nil
		}

		// An empty record is explicitly allowed:
		// https://tools.ietf.org/html/rfc7208#section-4.5
		if txt == "v=spf1" {
			return txt, nil
		}
	}

	return "", nil
}

func isTemporary(err error) bool {
	derr, ok := err.(*net.DNSError)
	return ok && derr.Temporary()
}

// ipField processes an "ip" field.
func (r *resolution) ipField(res Result, field string) (bool, Result, error) {
	fip := field[4:]
	if strings.Contains(fip, "/") {
		_, ipnet, err := net.ParseCIDR(fip)
		if err != nil {
			return true, PermError, errInvalidMask
		}
		if ipnet.Contains(r.ip) {
			return true, res, errMatchedIP
		}
	} else {
		ip := net.ParseIP(fip)
		if ip == nil {
			return true, PermError, errInvalidIP
		}
		if ip.Equal(r.ip) {
			return true, res, errMatchedIP
		}
	}

	return false, "", nil
}

// ptrField processes a "ptr" field.
func (r *resolution) ptrField(res Result, field, domain string) (bool, Result, error) {
	// Extract the domain if the field is in the form "ptr:domain"
	if len(field) >= 4 {
		domain = field[4:]

	}

	if r.ipNames == nil {
		r.count++
		n, err := lookupAddr(r.ip.String())
		if err != nil {
			// https://tools.ietf.org/html/rfc7208#section-5
			if isTemporary(err) {
				return true, TempError, err
			}
			return false, "", err
		}
		r.ipNames = n
	}

	for _, n := range r.ipNames {
		if strings.HasSuffix(n, domain+".") {
			return true, res, errMatchedPTR
		}
	}

	return false, "", nil
}

// includeField processes an "include" field.
func (r *resolution) includeField(res Result, field string) (bool, Result, error) {
	// https://tools.ietf.org/html/rfc7208#section-5.2
	incdomain := field[len("include:"):]
	ir, err := r.Check(incdomain)
	switch ir {
	case Pass:
		return true, res, err
	case Fail, SoftFail, Neutral:
		return false, ir, err
	case TempError:
		return true, TempError, err
	case PermError:
		return true, PermError, err
	case None:
		return true, PermError, errNoResult
	}

	return false, "", fmt.Errorf("This should never be reached")
}

func ipMatch(ip, tomatch net.IP, mask int) (bool, error) {
	if mask >= 0 {
		_, ipnet, err := net.ParseCIDR(fmt.Sprintf("%s/%d", tomatch.String(), mask))
		if err != nil {
			return false, errInvalidMask
		}
		if ipnet.Contains(ip) {
			return true, nil
		}
		return false, nil
	} else {
		if ip.Equal(tomatch) {
			return true, nil
		}
		return false, nil
	}
}

var aRegexp = regexp.MustCompile("a(:([^/]+))?(/(.+))?")
var mxRegexp = regexp.MustCompile("mx(:([^/]+))?(/(.+))?")

func domainAndMask(re *regexp.Regexp, field, domain string) (string, int, error) {
	var err error
	mask := -1
	if groups := re.FindStringSubmatch(field); groups != nil {
		if groups[2] != "" {
			domain = groups[2]
		}
		if groups[4] != "" {
			mask, err = strconv.Atoi(groups[4])
			if err != nil {
				return "", -1, errInvalidMask
			}
		}
	}

	return domain, mask, nil
}

// aField processes an "a" field.
func (r *resolution) aField(res Result, field, domain string) (bool, Result, error) {
	// https://tools.ietf.org/html/rfc7208#section-5.3
	domain, mask, err := domainAndMask(aRegexp, field, domain)
	if err != nil {
		return true, PermError, err
	}

	r.count++
	ips, err := lookupIP(domain)
	if err != nil {
		// https://tools.ietf.org/html/rfc7208#section-5
		if isTemporary(err) {
			return true, TempError, err
		}
		return false, "", err
	}
	for _, ip := range ips {
		ok, err := ipMatch(r.ip, ip, mask)
		if ok {
			return true, res, errMatchedA
		} else if err != nil {
			return true, PermError, err
		}
	}

	return false, "", nil
}

// mxField processes an "mx" field.
func (r *resolution) mxField(res Result, field, domain string) (bool, Result, error) {
	// https://tools.ietf.org/html/rfc7208#section-5.4
	domain, mask, err := domainAndMask(mxRegexp, field, domain)
	if err != nil {
		return true, PermError, err
	}

	r.count++
	mxs, err := lookupMX(domain)
	if err != nil {
		// https://tools.ietf.org/html/rfc7208#section-5
		if isTemporary(err) {
			return true, TempError, err
		}
		return false, "", err
	}
	mxips := []net.IP{}
	for _, mx := range mxs {
		r.count++
		ips, err := lookupIP(mx.Host)
		if err != nil {
			// https://tools.ietf.org/html/rfc7208#section-5
			if isTemporary(err) {
				return true, TempError, err
			}
			return false, "", err
		}
		mxips = append(mxips, ips...)
	}
	for _, ip := range mxips {
		ok, err := ipMatch(r.ip, ip, mask)
		if ok {
			return true, res, errMatchedMX
		} else if err != nil {
			return true, PermError, err
		}
	}

	return false, "", nil
}
