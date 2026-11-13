// Package dataformat recognizes bounded scalar formats at the collection source.
// A checksum confirms a format, not a real identity, account or statutory grade.
package dataformat

import (
	"regexp"
	"strings"
)

var (
	emailFormat    = regexp.MustCompile("^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9][A-Za-z0-9.-]*\\.[A-Za-z]{2,63}$")
	phoneFormat    = regexp.MustCompile(`^(\+86[- ]?)?1[3-9][0-9]{9}$`)
	nationalFormat = regexp.MustCompile(`^[1-9][0-9]{5}(18|19|20)[0-9]{2}(0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]$`)
	cardFormat     = regexp.MustCompile(`^[1-9][0-9]{15,18}$`)
)

func Matches(category, value string) bool {
	if len(value) > 256 {
		return false
	}
	switch category {
	case "email":
		return emailFormat.MatchString(value)
	case "phone":
		return phoneFormat.MatchString(value)
	case "national_id":
		if !nationalFormat.MatchString(value) {
			return false
		}
		weights := [...]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
		sum := 0
		for i, w := range weights {
			sum += int(value[i]-'0') * w
		}
		return strings.ToUpper(value[17:]) == string("10X98765432"[sum%11])
	case "financial_account":
		if !cardFormat.MatchString(value) || value == strings.Repeat(value[:1], len(value)) {
			return false
		}
		sum := 0
		for i := range value {
			n := int(value[i] - '0')
			if (len(value)-i)%2 == 0 {
				n *= 2
			}
			if n > 9 {
				n -= 9
			}
			sum += n
		}
		return sum%10 == 0
	}
	return false
}

func Categories(value string) []string {
	if len(value) > 256 {
		return nil
	}
	result := []string{}
	for _, category := range []string{"email", "phone", "national_id", "financial_account"} {
		if Matches(category, value) {
			result = append(result, category)
		}
	}
	return result
}
