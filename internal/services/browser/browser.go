// Package browser ports the shared-Chrome-instance half of
// browser-scrapers.ts to Go using chromedp in place of Puppeteer+
// puppeteer-extra-plugin-stealth (Risk R3 in the rewrite plan). The user's
// decision to accept a reliability gap here (some stealth-plugin evasion
// puppeteer-extra-stealth did - canvas/WebGL fingerprint spoofing, plugin
// list faking, etc. - has no chromedp equivalent) is already made; this
// package only implements the "low-effort subset" the plan calls out:
// a realistic User-Agent, --disable-blink-features=AutomationControlled,
// and a navigator.webdriver override evaluated into every new page.
package browser

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// stealthJS overrides navigator.webdriver (the single most common headless
// signal) before any page script runs. This is not a full stealth-plugin
// replacement - see the package doc and R3 in the rewrite plan.
const stealthJS = `Object.defineProperty(navigator, 'webdriver', { get: () => undefined });`

// DefaultTimeout bounds one scrape end-to-end, mirroring
// browser-scrapers.ts's DEFAULT_SCRAPE_TIMEOUT_MS - Puppeteer's own
// per-call timeouts only bound individual calls, so a page.evaluate() or a
// wedged renderer can otherwise hang forever.
const DefaultTimeout = 90 * time.Second

// sharedAllocator lazily starts one headless-Chrome allocator context for
// the whole process, matching getBrowser()'s "shared browser instance for
// better performance" - launching a fresh Chrome process per scrape is both
// slow (multi-second cold start) and memory-heavy under this app's
// systemd MemoryMax budget.
var (
	once           sync.Once
	allocCtx       context.Context
	allocCtxCancel context.CancelFunc
)

func sharedAllocator() context.Context {
	once.Do(func() {
		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-blink-features", "AutomationControlled"),
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-setuid-sandbox", true),
			chromedp.Flag("disable-dev-shm-usage", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.UserAgent(userAgent),
		)
		allocCtx, allocCtxCancel = chromedp.NewExecAllocator(context.Background(), opts...)
	})
	return allocCtx
}

// Close shuts down the shared Chrome process, if one was ever started.
// Intended for graceful-shutdown paths (main.go), not per-scrape use.
func Close() {
	if allocCtxCancel != nil {
		allocCtxCancel()
	}
}

// Scrape runs actions against a fresh tab (chromedp.NewContext) on the
// shared browser, injecting the webdriver-override script before
// navigation and enforcing an overall timeout - a direct port of
// runBrowserScrape's "one wrapper fixes the whole leaked-page/hung-call
// class of bug" reasoning. label is used only for the timeout error
// message (e.g. "Apple Music Browser").
func Scrape(ctx context.Context, label string, timeout time.Duration, actions ...chromedp.Action) error {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	tabCtx, cancel := chromedp.NewContext(sharedAllocator())
	defer cancel()

	tabCtx, timeoutCancel := context.WithTimeout(tabCtx, timeout)
	defer timeoutCancel()

	all := append([]chromedp.Action{chromedp.Evaluate(stealthJS, nil)}, actions...)
	if err := chromedp.Run(tabCtx, all...); err != nil {
		if tabCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%s timed out after %s", label, timeout)
		}
		return fmt.Errorf("%s failed: %w", label, err)
	}
	return nil
}

// AcceptLanguage returns a Scrape action that pins the tab's Accept-Language
// header, so a scraped page's title/metadata doesn't vary with wherever this
// server happens to run - opt-in per call site (prepend it to Scrape's own
// actions) rather than a default for every scraper, since only some callers
// (Collections' IMDb/Letterboxd list import, DESIGN.md §11.11 - "imported
// and processed in English only") need this; other scrapers are unaffected.
func AcceptLanguage(lang string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if err := network.Enable().Do(ctx); err != nil {
			return err
		}
		return network.SetExtraHTTPHeaders(network.Headers{"Accept-Language": lang}).Do(ctx)
	})
}

// AssertTracksScraped ports assertTracksScraped: a page whose selectors
// stopped matching (site redesign) still "succeeds" with zero tracks,
// which looks identical to a genuinely empty playlist unless callers are
// told to tell the two apart.
func AssertTracksScraped(count int, sourceLabel string) error {
	if count == 0 {
		return fmt.Errorf("%s found 0 tracks - the playlist may be empty/private, or the page structure may have changed and the scraper needs updating", sourceLabel)
	}
	return nil
}
