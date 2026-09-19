package toc

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ErrBlockedAddress は私設・ループバック・リンクローカル等への接続を拒否したこと。
var ErrBlockedAddress = errors.New("toc: blocked address")

// 公開インターネット以外の範囲（IsPrivate 等で判定できないもの）。
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
}

// PublicAddr は接続してよい公開アドレスかを返す。
func PublicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func isHTTPS(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// SafeFetcher は取得元ページを制限付きで取得する：https のみ、名前解決後の公開IPアドレスのみ、
// リダイレクト3回まで、5秒・2 MB まで。接続時のアドレスを検査するため、DNS の差し替えでも内部へ接続しない。
type SafeFetcher struct {
	client *http.Client
}

func NewSafeFetcher() *SafeFetcher {
	dialer := &net.Dialer{
		Timeout: fetchTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !PublicAddr(ap.Addr()) {
				return fmt.Errorf("%w: %s", ErrBlockedAddress, address)
			}
			return nil
		},
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   fetchTimeout,
		ResponseHeaderTimeout: fetchTimeout,
		MaxIdleConns:          4,
	}
	return &SafeFetcher{client: &http.Client{
		Timeout:   fetchTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > fetchMaxRedirect {
				return errors.New("toc: リダイレクトが多すぎます")
			}
			if req.URL.Scheme != "https" {
				return errors.New("toc: https 以外へのリダイレクト")
			}
			return nil
		},
	}}
}

func (f *SafeFetcher) Fetch(ctx context.Context, rawURL string) (string, error) {
	if !isHTTPS(rawURL) {
		return "", errors.New("toc: https の URL だけを取得します")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "reading-group-agent/0.1 (+toc verification)")
	res, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("toc: status %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, fetchMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > fetchMaxBytes {
		return "", errors.New("toc: ページが大きすぎます")
	}
	return HTMLText(string(body)), nil
}

var (
	scriptRe = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</(script|style|noscript)>`)
	tagRe    = regexp.MustCompile(`(?s)<[^>]*>`)
)

// HTMLText は HTML からタグを除いた本文を返す。
func HTMLText(s string) string {
	s = scriptRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	return html.UnescapeString(s)
}

// normalize は照合用に Unicode 正規化（NFKC）し、空白を除いて小文字にする。
func normalize(s string) string {
	s = norm.NFKC.String(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// MatchRatio は候補の項目名のうち、取得元ページの本文に含まれるものの割合を返す（部分一致）。
func MatchRatio(entries []Entry, pages []string) float64 {
	if len(entries) == 0 || len(pages) == 0 {
		return 0
	}
	var text strings.Builder
	for _, p := range pages {
		text.WriteString(normalize(p))
	}
	body := text.String()
	matched := 0
	for _, e := range entries {
		if t := normalize(e.Title); t != "" && strings.Contains(body, t) {
			matched++
		}
	}
	return float64(matched) / float64(len(entries))
}
