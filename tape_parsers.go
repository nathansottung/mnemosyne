package main

// tape_parsers.go — one small, defensive parser per supported tape tool. Each
// takes the tool's raw stdout and returns a TapeHealth with alerts + optional
// stats; the orchestrator (tape.go) derives severity/summary. Parsers are the
// tested unit — see testdata/ and tape_test.go. They must never panic on
// unexpected input: unknown lines are skipped, missing fields left zero.

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ---- shared helpers ----------------------------------------------------

// atoiLoose parses an integer that may carry thousands separators or units-free
// digits; returns 0 on failure.
func atoiLoose(s string) int64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	s = strings.Fields(s + " ")[0] // first token
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// parseSizeToBytes turns "1150 GB" / "500 MB" / "2 TB" into a byte count
// (decimal units). A bare number is treated as bytes. Best-effort.
func parseSizeToBytes(s string) int64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	m := reSize.FindStringSubmatch(s)
	if m == nil {
		return atoiLoose(s)
	}
	val, _ := strconv.ParseFloat(m[1], 64)
	mult := float64(1)
	switch strings.ToUpper(m[2]) {
	case "KB":
		mult = 1e3
	case "MB":
		mult = 1e6
	case "GB":
		mult = 1e9
	case "TB":
		mult = 1e12
	case "PB":
		mult = 1e15
	}
	return int64(val * mult)
}

var reSize = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*([KMGTP]B)`)

// flagsFromIDs classifies a set of active flag numbers, sorted ascending.
func flagsFromIDs(ids map[int]bool) []TapeAlertFlag {
	ordered := make([]int, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	// simple insertion sort (small n)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j-1] > ordered[j]; j-- {
			ordered[j-1], ordered[j] = ordered[j], ordered[j-1]
		}
	}
	out := make([]TapeAlertFlag, 0, len(ordered))
	for _, id := range ordered {
		out = append(out, classifyFlag(id))
	}
	return out
}

func usableTape(th TapeHealth) bool {
	return th.Vendor != "" || th.Product != "" || th.Serial != "" ||
		len(th.Alerts) > 0 || th.BytesWritten > 0 || th.BytesRead > 0 || th.PowerOnHours > 0
}

func scanLines(b []byte) *bufio.Scanner {
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return s
}

// ---- tapeinfo (sg3_utils) ---------------------------------------------

var reTapeinfoAlert = regexp.MustCompile(`TapeAlert\[(\d+)\]\s*:?\s*(.*)`)
var reQuoted = regexp.MustCompile(`'([^']*)'`)

func parseTapeinfo(b []byte) (TapeHealth, error) {
	th := TapeHealth{}
	ids := map[int]bool{}
	texts := map[int]string{}
	sc := scanLines(b)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "Vendor ID:"):
			th.Vendor = quotedOrRest(line)
		case strings.HasPrefix(line, "Product ID:"):
			th.Product = quotedOrRest(line)
		case strings.HasPrefix(line, "SerialNumber:"):
			th.Serial = quotedOrRest(line)
		default:
			if m := reTapeinfoAlert.FindStringSubmatch(line); m != nil {
				if id, err := strconv.Atoi(m[1]); err == nil {
					ids[id] = true
					texts[id] = strings.TrimSpace(m[2])
				}
			}
		}
	}
	th.Alerts = flagsFromIDs(ids)
	for i := range th.Alerts { // prefer the tool's own wording when present
		if t := texts[th.Alerts[i].ID]; t != "" {
			th.Alerts[i].Text = t
		}
	}
	if !usableTape(th) {
		return th, fmt.Errorf("tapeinfo: no usable drive data (is a tape drive attached?)")
	}
	return th, nil
}

func quotedOrRest(line string) string {
	if m := reQuoted.FindStringSubmatch(line); m != nil {
		return strings.TrimSpace(m[1])
	}
	if i := strings.Index(line, ":"); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	return ""
}

// ---- sg_logs (sg3_utils) ----------------------------------------------

// alertsPage = --page=0x2e ; statsPage = --page=0xc. Either may be empty.
var reSgParam = regexp.MustCompile(`(?i)parameter code\s*(?:=|:)?\s*(0x[0-9a-f]+|\d+)\D+?(\d+)\s*$`)

func parseSgLogs(alertsPage, statsPage []byte) (TapeHealth, error) {
	th := TapeHealth{}
	ids := map[int]bool{}

	sc := scanLines(alertsPage)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, p := headerVendorProduct(line); v != "" || p != "" {
			if th.Vendor == "" {
				th.Vendor, th.Product = v, p
			}
		}
		if m := reSgParam.FindStringSubmatch(line); m != nil {
			id := parseIntMaybeHex(m[1])
			active := m[2] != "0"
			if id > 0 && active {
				ids[id] = true
			}
		}
	}
	th.Alerts = flagsFromIDs(ids)

	sc = scanLines(statsPage)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		l := strings.ToLower(line)
		switch {
		case strings.Contains(l, "written to media"):
			th.BytesWritten = valueAfterColon(line)
		case strings.Contains(l, "read from media"):
			th.BytesRead = valueAfterColon(line)
		}
		if th.Vendor == "" {
			if v, p := headerVendorProduct(line); v != "" {
				th.Vendor, th.Product = v, p
			}
		}
	}
	if !usableTape(th) {
		return th, fmt.Errorf("sg_logs: no usable drive data (is a tape drive attached?)")
	}
	return th, nil
}

func valueAfterColon(line string) int64 {
	if i := strings.LastIndex(line, ":"); i >= 0 {
		return parseSizeToBytes(line[i+1:])
	}
	return 0
}

func parseIntMaybeHex(s string) int {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(strings.ToLower(s), "0x") {
		n, _ := strconv.ParseInt(s[2:], 16, 32)
		return int(n)
	}
	n, _ := strconv.Atoi(s)
	return n
}

// headerVendorProduct recognises an sg_logs device header line like
// "IBM       ULT3580-TD6       G9R1" (vendor, product, revision).
func headerVendorProduct(line string) (string, string) {
	if line == "" || strings.Contains(line, ":") || strings.Contains(strings.ToLower(line), "page") {
		return "", ""
	}
	f := strings.Fields(line)
	if len(f) >= 2 && looksLikeVendor(f[0]) {
		return f[0], f[1]
	}
	return "", ""
}

func looksLikeVendor(s string) bool {
	if len(s) < 2 || len(s) > 12 {
		return false
	}
	for _, r := range s {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

// ---- IBM ITDT ----------------------------------------------------------

var reItdtFlag = regexp.MustCompile(`(?i)flag\s+(\d+)`)
var reItdtSN = regexp.MustCompile(`(?i)S/N:?\s*([A-Za-z0-9]+)`)

func parseITDT(b []byte) (TapeHealth, error) {
	th := TapeHealth{}
	ids := map[int]bool{}
	sc := scanLines(b)
	inFlags := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		l := strings.ToLower(line)
		switch {
		case strings.HasPrefix(l, "drive:"):
			rest := strings.TrimSpace(line[len("drive:"):])
			if m := reItdtSN.FindStringSubmatch(rest); m != nil {
				th.Serial = m[1]
				rest = reItdtSN.ReplaceAllString(rest, "")
			}
			rest = regexp.MustCompile(`(?i)FW:\s*\S+`).ReplaceAllString(rest, "")
			f := strings.Fields(rest)
			if len(f) >= 1 {
				th.Vendor = f[0]
			}
			if len(f) >= 2 {
				th.Product = f[1]
			}
		case strings.HasPrefix(l, "power on hours:"):
			th.PowerOnHours = atoiLoose(line[strings.Index(line, ":")+1:])
		case strings.HasPrefix(l, "total bytes written:"):
			th.BytesWritten = parseSizeToBytes(line[strings.Index(line, ":")+1:])
		case strings.HasPrefix(l, "total bytes read:"):
			th.BytesRead = parseSizeToBytes(line[strings.Index(line, ":")+1:])
		case strings.Contains(l, "tapealert"):
			inFlags = true
		default:
			if inFlags {
				if m := reItdtFlag.FindStringSubmatch(line); m != nil {
					if id, err := strconv.Atoi(m[1]); err == nil {
						ids[id] = true
					}
				}
			}
		}
	}
	th.Alerts = flagsFromIDs(ids)
	if !usableTape(th) {
		return th, fmt.Errorf("itdt: no usable drive data (is a tape drive attached?)")
	}
	return th, nil
}

// ---- HPE Library & Tape Tools -----------------------------------------

func parseHpLtt(b []byte) (TapeHealth, error) {
	th := TapeHealth{}
	ids := map[int]bool{}
	sc := scanLines(b)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v := splitKV(line)
		switch strings.ToLower(k) {
		case "vendor":
			th.Vendor = v
		case "product":
			th.Product = v
		case "serial number", "serial":
			th.Serial = v
		case "power on hours":
			th.PowerOnHours = atoiLoose(v)
		case "total bytes written", "bytes written":
			th.BytesWritten = parseSizeToBytes(v)
		case "total bytes read", "bytes read":
			th.BytesRead = parseSizeToBytes(v)
		case "tapealert active flags", "tapealert flags", "active flags":
			for _, tok := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
				if n, err := strconv.Atoi(strings.TrimSpace(tok)); err == nil {
					ids[n] = true
				}
			}
		}
	}
	th.Alerts = flagsFromIDs(ids)
	if !usableTape(th) {
		return th, fmt.Errorf("hpe-ltt: no usable drive data (is a tape drive attached?)")
	}
	return th, nil
}

func splitKV(line string) (string, string) {
	i := strings.Index(line, ":")
	if i < 0 {
		return "", ""
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
}
