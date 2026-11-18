// Package browser runs provider scrapes in isolated tabs in a sandboxed Chrome.
package browser

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// DefaultTimeout bounds one scrape end-to-end, mirroring
// browser-scrapers.ts's DEFAULT_SCRAPE_TIMEOUT_MS - Puppeteer's own
// per-call timeouts only bound individual calls, so a page.evaluate() or a
// wedged renderer can otherwise hang forever.
const DefaultTimeout = 90 * time.Second

var (
	browserMu   sync.Mutex
	browserCtx  context.Context
	stopBrowser context.CancelFunc
)

func sharedBrowser() (context.Context, error) {
	browserMu.Lock()
	defer browserMu.Unlock()
	if browserCtx != nil {
		if browser := chromedp.FromContext(browserCtx).Browser; browser != nil {
			select {
			case <-browser.LostConnection:
				stopBrowser()
				browserCtx = nil
			default:
				return browserCtx, nil
			}
		}
	}
	if os.Geteuid() == 0 {
		return nil, fmt.Errorf("sandboxed browser scrapes require Playlist Lab to run as a non-root user")
	}

	opts := []chromedp.ExecAllocatorOption{
		chromedp.Headless,
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("no-sandbox", false),
		chromedp.Flag("disable-background-networking", true),
		chromedp.UserAgent(userAgent),
	}
	allocator, stop := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocator)
	startupTimeout := time.AfterFunc(15*time.Second, stop)
	err := chromedp.Run(ctx)
	inTime := startupTimeout.Stop()
	if err != nil || !inTime {
		cancel()
		stop()
		if err == nil {
			err = context.DeadlineExceeded
		}
		return nil, err
	}
	browserCtx = ctx
	stopBrowser = func() { cancel(); stop() }
	return browserCtx, nil
}

func Close() {
	browserMu.Lock()
	defer browserMu.Unlock()
	if browserCtx != nil {
		stopBrowser()
		browserCtx = nil
	}
}

func allowedURL(rawURL string, domains []string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, domain := range domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// Scrape limits navigation, redirects, and HTTP subresources to the
// provider's HTTPS hosts. Each scrape gets an isolated browser context.
func Scrape(ctx context.Context, label string, timeout time.Duration, pageURL string, domains []string, actions ...chromedp.Action) error {
	if !allowedURL(pageURL, domains) {
		return fmt.Errorf("%s requires an HTTPS URL on the provider domain", label)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, err := sharedBrowser()
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	deadline, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	executor := cdp.WithExecutor(deadline, chromedp.FromContext(parent).Browser)
	profile, err := target.CreateBrowserContext().WithDisposeOnDetach(true).Do(executor)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = target.DisposeBrowserContext(profile).Do(cdp.WithExecutor(ctx, chromedp.FromContext(parent).Browser))
	}()
	tab, err := target.CreateTarget("about:blank").WithBrowserContextID(profile).WithNewWindow(true).Do(executor)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	tabCtx, closeTab := chromedp.NewContext(deadline, chromedp.WithTargetID(tab))
	defer closeTab()

	chromedp.ListenTarget(tabCtx, func(event any) {
		paused, ok := event.(*fetch.EventRequestPaused)
		if !ok {
			return
		}
		go func() {
			executor := cdp.WithExecutor(tabCtx, chromedp.FromContext(tabCtx).Target)
			if !allowedURL(paused.Request.URL, domains) {
				_ = fetch.FailRequest(paused.RequestID, network.ErrorReasonBlockedByClient).Do(executor)
				return
			}
			_ = fetch.ContinueRequest(paused.RequestID).Do(executor)
		}()
	})

	if err := chromedp.Run(tabCtx, network.Enable(), network.SetBypassServiceWorker(true), network.SetExtraHTTPHeaders(network.Headers{"Accept-Language": "en-US,en;q=0.9"}), fetch.Enable()); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	actions = append([]chromedp.Action{chromedp.Navigate(pageURL)}, actions...)
	if err := chromedp.Run(tabCtx, actions...); err != nil {
		return fmt.Errorf("%s failed: %w", label, err)
	}
	return nil
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
