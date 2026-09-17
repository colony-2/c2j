package execution

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

var (
	cpuSyntax  = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(m?)$`)
	byteSyntax = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)([kMGTPE]|[KMGTPE]i)$`)
)

func normalizeCPU(value string) (string, error) {
	_, scalar, err := parseQuantity(value, true)
	if err != nil {
		return "", err
	}
	// Quantity.String may use DecimalExponent for some inputs; explicitly keep
	// the public CPU vocabulary in cores/millicores.
	if scalar%1000 == 0 {
		return fmt.Sprint(scalar / 1000), nil
	}
	return fmt.Sprintf("%dm", scalar), nil
}

func normalizeBytes(value string) (string, error) {
	quantity, scalar, err := parseQuantity(value, false)
	if err != nil {
		return "", err
	}
	canonical := quantity.String()
	// Quantity.String drops the suffix for small integral values. Our public
	// grammar always requires a unit, so express those in kilobytes instead.
	if !byteSyntax.MatchString(canonical) {
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d.%03d", scalar/1000, scalar%1000), "0"), ".") + "k", nil
	}
	return canonical, nil
}

func parseQuantity(value string, cpu bool) (resource.Quantity, int64, error) {
	invalid := func() (resource.Quantity, int64, error) {
		unit := "positive integral bytes with an explicit SI/IEC unit"
		if cpu {
			unit = "positive cores or millicores with at most millicore precision"
		}
		return resource.Quantity{}, 0, fmt.Errorf("%q must represent %s within int64 range", value, unit)
	}
	// Bound parsing work for untrusted metadata and payloads.
	if len(value) > 128 {
		return invalid()
	}
	syntax := byteSyntax
	if cpu {
		syntax = cpuSyntax
	}
	parts := syntax.FindStringSubmatch(value)
	if parts == nil {
		return invalid()
	}
	q, err := resource.ParseQuantity(value)
	if err != nil {
		return invalid()
	}
	// ParseQuantity deliberately rounds sub-nano values and caps overflowing
	// BinarySI values. Check against an exact rational to reject either rather
	// than accepting a silently weakened requirement.
	exact, ok := new(big.Rat).SetString(parts[1])
	if !ok || exact.Sign() <= 0 {
		return invalid()
	}
	suffix := parts[2]
	multiplier := new(big.Int).SetInt64(1)
	if cpu {
		if suffix == "m" {
			exact.Quo(exact, big.NewRat(1000, 1))
		}
	} else {
		power := strings.Index("kMGTPE", strings.ReplaceAll(suffix[:1], "K", "k")) + 1
		base := int64(1000)
		if strings.HasSuffix(suffix, "i") {
			base = 1024
		}
		multiplier.Exp(big.NewInt(base), big.NewInt(int64(power)), nil)
		exact.Mul(exact, new(big.Rat).SetInt(multiplier))
	}
	parsed, ok := new(big.Rat).SetString(q.AsDec().String())
	if !ok || exact.Cmp(parsed) != 0 {
		return invalid()
	}
	if cpu {
		exact.Mul(exact, big.NewRat(1000, 1))
	}
	if !exact.IsInt() || !exact.Num().IsInt64() {
		return invalid()
	}
	return q, exact.Num().Int64(), nil
}
