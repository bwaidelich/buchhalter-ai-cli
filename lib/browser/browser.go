package browser

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"buchhalter/lib/archive"
	"buchhalter/lib/parser"
	"buchhalter/lib/utils"
	"buchhalter/lib/vault"

	cu "github.com/Davincible/chromedp-undetected"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

var (
	textStyleBold = lipgloss.NewStyle().Bold(true).Render
)

type BrowserDriver struct {
	logger          *slog.Logger
	credentials     *vault.Credentials
	documentArchive *archive.DocumentArchive

	buchhalterDirectory string

	ChromeVersion string

	// TODO Check if those are needed
	downloadsDirectory string
	documentsDirectory string

	browserCtx    context.Context
	recipeTimeout time.Duration
	newFilesCount int
}

func NewBrowserDriver(logger *slog.Logger, credentials *vault.Credentials, buchhalterDirectory string, documentArchive *archive.DocumentArchive) *BrowserDriver {
	return &BrowserDriver{
		logger:          logger,
		credentials:     credentials,
		documentArchive: documentArchive,

		buchhalterDirectory: buchhalterDirectory,

		browserCtx:    context.Background(),
		recipeTimeout: 60 * time.Second,
		newFilesCount: 0,
	}
}

func (b *BrowserDriver) RunRecipe(p *tea.Program, tsc int, scs int, bcs int, recipe *parser.Recipe) utils.RecipeResult {
	// Init browser
	b.logger.Info("Starting chrome browser driver ...", "recipe", recipe.Provider, "recipe_version", recipe.Version)
	ctx, cancel, err := cu.New(cu.NewConfig(
		cu.WithContext(b.browserCtx),
	))
	if err != nil {
		// TODO Implement error handling
		panic(err)
	}
	defer cancel()

	// create a timeout as a safety net to prevent any infinite wait loops
	ctx, cancel = context.WithTimeout(ctx, 600*time.Second)
	defer cancel()

	// get chrome version for metrics
	if b.ChromeVersion == "" {
		err := chromedp.Run(ctx, chromedp.Tasks{
			chromedp.Navigate("chrome://version"),
			chromedp.Text(`#version`, &b.ChromeVersion, chromedp.NodeVisible),
		})
		if err != nil {
			// TODO Implement error handling
			panic(err)
		}
		b.ChromeVersion = strings.TrimSpace(b.ChromeVersion)
	}
	b.logger.Info("Starting chrome browser driver ... completed ", "recipe", recipe.Provider, "recipe_version", recipe.Version, "chrome_version", b.ChromeVersion)

	// create download directories
	b.downloadsDirectory, b.documentsDirectory, err = utils.InitProviderDirectories(b.buchhalterDirectory, recipe.Provider)
	if err != nil {
		// TODO Implement error handling
		fmt.Println(err)
	}
	b.logger.Info("Download directories created", "downloads_directory", b.downloadsDirectory, "documents_directory", b.documentsDirectory)

	err = chromedp.Run(ctx, chromedp.Tasks{
		browser.
			SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllow).
			WithDownloadPath(b.downloadsDirectory).
			WithEventsEnabled(true),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// TODO Implement error handling
			_ = b.waitForLoadEvent(ctx)
			return nil
		}),
	})
	if err != nil {
		// TODO Implement error handling
		panic(err)
	}

	// Disable downloading images for performance reasons
	chromedp.ListenTarget(ctx, b.disableImages(ctx))

	_ = b.enableLifeCycleEvents()

	var cs float64
	n := 1
	var result utils.RecipeResult
	for _, step := range recipe.Steps {
		sr := make(chan utils.StepResult, 1)
		p.Send(utils.ResultTitleAndDescriptionUpdate{Title: "Downloading invoices from " + recipe.Provider + " (" + strconv.Itoa(n) + "/" + strconv.Itoa(scs) + "):", Description: step.Description})
		/** Timeout recipe if something goes wrong */
		go func() {
			switch action := step.Action; action {
			case "open":
				sr <- b.stepOpen(ctx, step)
			case "removeElement":
				sr <- b.stepRemoveElement(ctx, step)
			case "click":
				sr <- b.stepClick(ctx, step)
			case "type":
				sr <- b.stepType(ctx, step, b.credentials)
			case "sleep":
				sr <- b.stepSleep(ctx, step)
			case "waitFor":
				sr <- b.stepWaitFor(ctx, step)
			case "downloadAll":
				sr <- b.stepDownloadAll(ctx, step)
			case "transform":
				sr <- b.stepTransform(step)
			case "move":
				sr <- b.stepMove(step, b.documentArchive)
			case "runScript":
				sr <- b.stepRunScript(ctx, step)
			case "runScriptDownloadUrls":
				sr <- b.stepRunScriptDownloadUrls(ctx, step)
			}
		}()

		select {
		case lsr := <-sr:
			newDocumentsText := strconv.Itoa(b.newFilesCount) + " new documents"
			if b.newFilesCount == 1 {
				newDocumentsText = "One new document"
			}
			if b.newFilesCount == 0 {
				newDocumentsText = "No new documents"
			}
			if lsr.Status == "success" {
				result = utils.RecipeResult{
					Status:              "success",
					StatusText:          recipe.Provider + ": " + newDocumentsText,
					StatusTextFormatted: "- " + textStyleBold(recipe.Provider) + ": " + newDocumentsText,
					LastStepId:          recipe.Provider + "-" + recipe.Version + "-" + strconv.Itoa(n) + "-" + step.Action,
					LastStepDescription: step.Description,
					NewFilesCount:       b.newFilesCount,
				}
			} else {
				result = utils.RecipeResult{
					Status:              "error",
					StatusText:          recipe.Provider + "aborted with error.",
					StatusTextFormatted: "x " + textStyleBold(recipe.Provider) + " aborted with error.",
					LastStepId:          recipe.Provider + "-" + recipe.Version + "-" + strconv.Itoa(n) + "-" + step.Action,
					LastStepDescription: step.Description,
					LastErrorMessage:    lsr.Message,
					NewFilesCount:       b.newFilesCount,
				}
				err = utils.TruncateDirectory(b.downloadsDirectory)
				if err != nil {
					// TODO Implement error handling
					fmt.Println(err)
				}
				return result
			}

		case <-time.After(b.recipeTimeout):
			result = utils.RecipeResult{
				Status:              "error",
				StatusText:          recipe.Provider + " aborted with timeout.",
				StatusTextFormatted: "x " + textStyleBold(recipe.Provider) + " aborted with timeout.",
				LastStepId:          recipe.Provider + "-" + recipe.Version + "-" + strconv.Itoa(n) + "-" + step.Action,
				LastStepDescription: step.Description,
				NewFilesCount:       b.newFilesCount,
			}
			err = utils.TruncateDirectory(b.downloadsDirectory)
			if err != nil {
				// TODO Implement error handling
				fmt.Println(err)
			}
			return result
		}
		cs = (float64(bcs) + float64(n)) / float64(tsc)
		p.Send(utils.ResultProgressUpdate{Percent: cs})
		n++
	}

	err = utils.TruncateDirectory(b.downloadsDirectory)
	if err != nil {
		// TODO Implement error handling
		fmt.Println(err)
	}
	return result
}

func (b *BrowserDriver) Quit() error {
	if b.browserCtx != nil {
		return chromedp.Cancel(b.browserCtx)
	}

	return nil
}

func (b *BrowserDriver) stepOpen(ctx context.Context, step parser.Step) utils.StepResult {
	b.logger.Debug("Executing recipe step", "action", step.Action, "url", step.URL)

	if err := chromedp.Run(ctx,
		// navigate to the page
		chromedp.Navigate(step.URL),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_ = b.waitForLoadEvent(ctx)
			return nil
		}),
	); err != nil {
		return utils.StepResult{Status: "error", Message: err.Error()}
	}
	return utils.StepResult{Status: "success"}
}

func (b *BrowserDriver) stepRemoveElement(ctx context.Context, step parser.Step) utils.StepResult {
	b.logger.Debug("Executing recipe step", "action", step.Action, "selector", step.Selector)

	nodeName := "node" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := chromedp.Run(ctx,
		chromedp.Evaluate("let "+nodeName+" = document.querySelector('"+step.Selector+"'); "+nodeName+".parentNode.removeChild("+nodeName+")", nil),
	); err != nil {
		return utils.StepResult{Status: "error", Message: err.Error()}
	}
	return utils.StepResult{Status: "success"}
}

func (b *BrowserDriver) stepClick(ctx context.Context, step parser.Step) utils.StepResult {
	b.logger.Debug("Executing recipe step", "action", step.Action, "selector", step.Selector)

	if err := chromedp.Run(ctx,
		chromedp.Click(step.Selector, chromedp.NodeReady),
	); err != nil {
		return utils.StepResult{Status: "error", Message: err.Error()}
	}
	return utils.StepResult{Status: "success"}
}

func (b *BrowserDriver) stepType(ctx context.Context, step parser.Step, credentials *vault.Credentials) utils.StepResult {
	b.logger.Debug("Executing recipe step", "action", step.Action, "selector", step.Selector, "value", step.Value)

	step.Value = b.parseCredentialPlaceholders(step.Value, credentials)
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(step.Selector, step.Value, chromedp.NodeReady),
	); err != nil {
		return utils.StepResult{Status: "error", Message: err.Error()}
	}
	return utils.StepResult{Status: "success"}
}

func (b *BrowserDriver) stepSleep(ctx context.Context, step parser.Step) utils.StepResult {
	b.logger.Debug("Executing recipe step", "action", step.Action, "length", step.Value)

	seconds, _ := strconv.Atoi(step.Value)
	if err := chromedp.Run(ctx,
		chromedp.Sleep(time.Duration(seconds)*time.Second),
	); err != nil {
		return utils.StepResult{Status: "error", Message: err.Error()}
	}
	return utils.StepResult{Status: "success"}
}

func (b *BrowserDriver) stepWaitFor(ctx context.Context, step parser.Step) utils.StepResult {
	b.logger.Debug("Executing recipe step", "action", step.Action, "selector", step.Selector)

	if err := chromedp.Run(ctx,
		chromedp.WaitReady(step.Selector),
	); err != nil {
		return utils.StepResult{Status: "error", Message: err.Error()}
	}
	return utils.StepResult{Status: "success"}
}

func (b *BrowserDriver) stepDownloadAll(ctx context.Context, step parser.Step) utils.StepResult {
	b.logger.Debug("Executing recipe step", "action", step.Action, "selector", step.Selector)

	var nodes []*cdp.Node
	err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.WaitReady(step.Selector),
		chromedp.Nodes(step.Selector, &nodes),
	})
	if err != nil {
		return utils.StepResult{Status: "error", Message: err.Error()}
	}

	wg := &sync.WaitGroup{}
	chromedp.ListenTarget(ctx, func(v interface{}) {
		switch ev := v.(type) {
		case *browser.EventDownloadWillBegin:
			b.logger.Debug("Executing recipe step ... download begins", "action", step.Action, "guid", ev.GUID, "url", ev.URL)
		case *browser.EventDownloadProgress:
			if ev.State == browser.DownloadProgressStateCompleted {
				b.logger.Debug("Executing recipe step ... download completed", "action", step.Action, "guid", ev.GUID)
				go func() {
					wg.Done()
				}()
			}
		}
	})

	// Click on link (for client-side js stuff)
	// Limit nodes to 2 to prevent too many downloads at once/rate limiting
	dl := len(nodes)
	if dl > 2 {
		dl = 2
	}
	wg.Add(dl)
	x := 0
	for _, n := range nodes {
		// TODO: We only download the latest two files for now. This should be configurable in the future.
		if x >= 2 {
			break
		}

		b.logger.Debug("Executing recipe step ... trigger download click", "action", step.Action, "selector", n.FullXPath()+step.Value)
		if err := chromedp.Run(ctx, fetch.Enable(), chromedp.Tasks{
			chromedp.MouseClickNode(n),
			chromedp.WaitVisible(n.FullXPath() + step.Value),
			chromedp.Click(n.FullXPath() + step.Value),
		}); err != nil {
			return utils.StepResult{Status: "error", Message: err.Error()}
		}
		// Delay clicks to prevent too many downloads at once/rate limiting
		time.Sleep(1500 * time.Millisecond)
		x++
	}
	wg.Wait()

	b.logger.Debug("Executing recipe step ... downloads completed", "action", step.Action)
	b.logger.Info("All downloads completed")

	return utils.StepResult{Status: "success"}
}

func (b *BrowserDriver) stepTransform(step parser.Step) utils.StepResult {
	b.logger.Debug("Executing recipe step", "action", step.Action, "value", step.Value)

	switch step.Value {
	case "unzip":
		zipFiles, err := utils.FindFiles(b.downloadsDirectory, ".zip")
		if err != nil {
			// TODO improve error handling
			fmt.Println(err)
		}
		for _, s := range zipFiles {
			b.logger.Debug("Executing recipe step ... unzipping file", "action", step.Action, "source", s, "destination", b.downloadsDirectory)
			b.logger.Info("Unzipping file", "source", s, "destination", b.downloadsDirectory)
			err := utils.UnzipFile(s, b.downloadsDirectory)
			if err != nil {
				return utils.StepResult{Status: "error", Message: err.Error()}
			}
		}
	}

	return utils.StepResult{Status: "success"}
}

func (b *BrowserDriver) stepMove(step parser.Step, documentArchive *archive.DocumentArchive) utils.StepResult {
	b.logger.Debug("Executing recipe step", "action", step.Action, "value", step.Value)

	b.newFilesCount = 0
	err := filepath.WalkDir(b.downloadsDirectory, func(s string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		match, e := regexp.MatchString(step.Value, d.Name())
		if e != nil {
			return e
		}
		if match {
			srcFile := filepath.Join(b.downloadsDirectory, d.Name())
			// Check if file already exists
			if !documentArchive.FileExists(srcFile) {
				b.logger.Debug("Executing recipe step ... moving file", "action", step.Action, "source", srcFile, "destination", filepath.Join(b.documentsDirectory, d.Name()))
				b.logger.Info("Moving file", "source", srcFile, "destination", filepath.Join(b.documentsDirectory, d.Name()))
				b.newFilesCount++
				_, err := utils.CopyFile(srcFile, filepath.Join(b.documentsDirectory, d.Name()))
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return utils.StepResult{Status: "error", Message: err.Error()}
	}

	return utils.StepResult{Status: "success"}
}

func (b *BrowserDriver) stepRunScript(ctx context.Context, step parser.Step) utils.StepResult {
	b.logger.Debug("Executing recipe step", "action", step.Action, "value", step.Value)

	var res []string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(step.Value, &res),
	); err != nil {
		return utils.StepResult{Status: "error", Message: err.Error()}
	}
	return utils.StepResult{Status: "success"}
}

func (b *BrowserDriver) stepRunScriptDownloadUrls(ctx context.Context, step parser.Step) utils.StepResult {
	b.logger.Debug("Executing recipe step", "action", step.Action, "value", step.Value)

	var res []string
	chromedp.Evaluate(`Object.values(`+step.Value+`);`, &res)
	for _, url := range res {
		b.logger.Debug("Executing recipe step ... download", "action", step.Action, "url", url)
		if err := chromedp.Run(ctx,
			browser.
				SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllowAndName).
				WithDownloadPath(b.downloadsDirectory).
				WithEventsEnabled(true),
			chromedp.Navigate(url),
			chromedp.ActionFunc(func(ctx context.Context) error {
				_ = b.waitForLoadEvent(ctx)
				return nil
			}),
		); err != nil {
			return utils.StepResult{Status: "error", Message: err.Error()}
		}
	}

	return utils.StepResult{Status: "success"}
}

func (b *BrowserDriver) parseCredentialPlaceholders(value string, credentials *vault.Credentials) string {
	value = strings.Replace(value, "{{ username }}", credentials.Username, -1)
	value = strings.Replace(value, "{{ password }}", credentials.Password, -1)
	value = strings.Replace(value, "{{ totp }}", credentials.Totp, -1)
	return value
}

func (b *BrowserDriver) disableImages(ctx context.Context) func(event interface{}) {
	return func(event interface{}) {
		switch ev := event.(type) {
		case *fetch.EventRequestPaused:
			go func() {
				c := chromedp.FromContext(ctx)
				ctx := cdp.WithExecutor(ctx, c.Target)
				if ev.ResourceType == network.ResourceTypeImage {
					err := fetch.FailRequest(ev.RequestID, network.ErrorReasonBlockedByClient).Do(ctx)
					if err != nil {
						b.logger.Debug("Failed to block image request", "error", err.Error())
						return
					}
				} else {
					err := fetch.ContinueRequest(ev.RequestID).Do(ctx)
					if err != nil {
						b.logger.Debug("Failed to continue request", "error", err.Error())
						return
					}
				}
			}()
		}
	}
}

func (b *BrowserDriver) enableLifeCycleEvents() chromedp.ActionFunc {
	return func(ctx context.Context) error {
		err := page.Enable().Do(ctx)
		if err != nil {
			return err
		}
		err = page.SetLifecycleEventsEnabled(true).Do(ctx)
		if err != nil {
			return err
		}
		return nil
	}
}

func (b *BrowserDriver) waitForLoadEvent(ctx context.Context) error {
	ch := make(chan struct{})
	cctx, cancel := context.WithCancel(ctx)

	chromedp.ListenTarget(cctx, func(ev interface{}) {
		switch e := ev.(type) {
		case *page.EventLifecycleEvent:
			if e.Name == "networkIdle" {
				cancel()
				close(ch)
			}
		}
	})

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
