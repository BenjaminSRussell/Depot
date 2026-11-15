package domain

import (
	"regexp"
	"strings"
)

var carrierServiceCodeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)

// CarrierService is one service a carrier sells (such as "UPS Ground"): the master that rate cards and
// rate shopping name by id. Code is unique per carrier.
type CarrierService struct {
	ID        string `json:"id"`
	CarrierID string `json:"carrierId"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	Active    bool   `json:"active"`
}

// Validate returns the field errors of a service about to be created.
func (c CarrierService) Validate() map[string]string {
	fields := map[string]string{}
	if !lowerUUID.MatchString(c.CarrierID) {
		fields["carrierId"] = "must be a lower-case UUID"
	}
	if !carrierServiceCodeRe.MatchString(c.Code) {
		fields["code"] = "must be 1 to 20 upper-case letters, digits, dashes or underscores"
	}
	if n := len([]rune(strings.TrimSpace(c.Name))); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	valid := false
	for _, m := range LoadModes {
		valid = valid || m == c.Mode
	}
	if !valid {
		fields["mode"] = "must be one of " + strings.Join(LoadModes, ", ")
	}
	return fields
}
