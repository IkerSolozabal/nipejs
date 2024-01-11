package nipejs

import (
	"bufio"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/user"
	"strings"
	"sync"
	"time"

	. "github.com/logrusorgru/aurora/v3"
	log "github.com/projectdiscovery/gologger"
	"github.com/projectdiscovery/gologger/levels"
	"github.com/valyala/fasthttp"
	"go.elara.ws/pcre"
)

var (
	regexf = flag.String("r", "~/.config/nipejs/regex.txt", "Regex file")
	usera  = flag.String(
		"a",
		"Mozilla/5.0 (Windows NT 12.0; rv:88.0) Gecko/20100101 Firefox/88.0",
		"User-Agent",
	)
	silent     = flag.Bool("s", false, "Silent Mode")
	threads    = flag.Int("c", 50, "Set the concurrency level")
	urls       = flag.String("u", "", "List of URLs to scan")
	debug      = flag.Bool("b", false, "Debug mode")
	timeout    = flag.Int("timeout", 10, "Timeout in seconds")
	version    = flag.Bool("version", false, "Prints version information")
	jsdir      = flag.String("d", "", "Directory to scan all the files")
	Scan       = flag.Bool("no-scan", false, "Disable all scans for Special Regexs")
	jsonOutput = flag.Bool("json", false, "Enable json output")
)

var wg sync.WaitGroup

type Results struct {
	Match         string  `json:"Match"`
	Url           string  `json:"Url"`
	Regex         string  `json:"Regex"`
	Category      string  `json:"Category"`
	ContentLength float64 `json:"ContentLength"`
}

func init() {
	flag.Parse()

	if *debug {
		log.DefaultLogger.SetMaxLevel(levels.LevelDebug)
	}
	if *version {
		fmt.Printf("NipeJS %s\n", Version)
		os.Exit(1)
	}

	if !*silent {
		Banner()
	} else {
		log.DefaultLogger.SetMaxLevel(levels.LevelSilent)
	}
	if *regexf == "~/.config/nipejs/regex.txt" {
		user, err := user.Current()
		if err != nil {
			log.Fatal().Msgf("%s", err)
		}
		*regexf = fmt.Sprintf("%s/.config/nipejs/regex.txt", user.HomeDir)
	}
}

func Execute() {
	err := FirstTime()
	if err != nil {
		log.Error().Msgf("%v", err)
	}

	// Configs
	c := &fasthttp.Client{
		Name: *usera,
		TLSConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		MaxConnWaitTimeout: time.Duration(*timeout) * time.Second,
	}
	urlsFile, _ := os.Open(*urls)

	checkRegexs(*regexf)
	allRegex, _ := countLines(*regexf)

	results := make(chan Results, *threads)
	curl := make(chan string, *threads)

	var input *bufio.Scanner
	var thread, countFiles, totalScan int

	StartTimestamp := time.Now().UnixNano()
	tmpFilename := fmt.Sprintf("/tmp/nipejs_%d%d", StartTimestamp, rand.Intn(100))

	// Switch case that define the Input Type
	switch {
	// If the input is STDIN (-u, -f or -d not especified)
	case *urls == "" && *jsdir == "":
		log.Debug().Msg("define input as Stdin")
		log.Debug().Msgf("Threads open: %d", *threads)
		for w := 0; w < *threads; w++ {
			go GetBody(curl, results, c)
		}
		input = bufio.NewScanner(os.Stdin)

	// If the input is for urls (-u especified)
	case *jsdir == "" && *urls != "":
		lines, _ := countLines(*urls)
		if lines < *threads {
			thread = lines
		} else {
			thread = *threads
		}
		log.Debug().Msgf("Threads open: %d", thread)
		for w := 0; w < thread; w++ {
			go GetBody(curl, results, c)
		}
		input = bufio.NewScanner(urlsFile)

		// If the input is for file or folder (-d)
	case *jsdir != "" && *urls == "":
		log.Debug().Msg(*jsdir)
		fileInfo, err := os.Stat(*jsdir)
		if err != nil {
			log.Fatal().Msg("Could not open Directory")
		}

		var tmpFile io.Reader

		// Scan a full Folder
		if fileInfo.IsDir() {
			tmpFile, countFiles = scanFolder(tmpFilename, *jsdir) // For directories
			if countFiles < *threads {
				thread = countFiles
			} else {
				thread = *threads
			}
			// Scan only one file
		} else {
			tmpFile = createTMPfile(tmpFilename, []string{*jsdir}) // For file
			thread = 1
		}
		defer os.Remove(tmpFilename)

		log.Debug().Msgf("Threads open: %d", thread)

		// Gouroutines That will wait for the input on channel 'curl'
		for w := 0; w < thread; w++ {
			go ReadFiles(results, curl)
		}

		input = bufio.NewScanner(tmpFile)

	default:
		log.Fatal().Msg("You can only specify one input method (-d or -u).")
	}

	go func() {
		for {
			resp := <-results
			switch resp.Regex {
			case `AAAA[A-Za-z0-9_-]{7}:[A-Za-z0-9_-]{140}`:
				resp.printDefault("Firebase")

			case `sq0csp-[ 0-9A-Za-z\-_]{43}|sq0[a-z]{3}-[0-9A-Za-z\-_]{22,43}`:
				resp.printDefault("Square oauth secret")

			case `sqOatp-[0-9A-Za-z\-_]{22}|EAAA[a-zA-Z0-9]{60}`:
				resp.printDefault("Square access token")

			case `AC[a-zA-Z0-9_\-]{32}`:
				resp.printDefault("Twilio account SID")

			case `AP[a-zA-Z0-9_\-]{32}`:
				resp.printDefault("Twilio APP SID")

			case `[A-Za-z0-9]{125}`:
				resp.printDefault("Facebook")

			case `s3\.amazonaws.com[/]+|[a-zA-Z0-9_-]*\.s3\.amazonaws.com`:
				resp.printDefault("S3 bucket")

			case `\b(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)(\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)){3}\b`:
				resp.printDefault("IPv4")

			case `[a-f0-9]{32}`:
				resp.printDefault("MD5 hash")

			case `6L[0-9A-Za-z-_]{38}|^6[0-9a-zA-Z_-]{39}`:
				resp.printSpecific("Google Recaptcha")

			case `key-[0-9a-zA-Z]{32}`:
				resp.printSpecific("Mailgun")

			case `[0-9a-f]{8}-[0-9a-f]{4}-[0-5][0-9a-f]{3}-[089ab][0-9a-f]{3}-[0-9a-f]{12}`,
				`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`:
				resp.printDefault("UUID")

			case `(eyJ|YTo|Tzo|PD[89]|aHR0cHM6L|aHR0cDo|rO0)[a-zA-Z0-9+/]+={0,2}`:
				resp.printSpecific("Base64")

			case `<h1>Index of (.*?)</h1>`:
				resp.printDefault("Index page")

			case "":
				break

			default:
				resp.printDefault("")
			}
		}
	}()
	for input.Scan() {
		// Send the input value to the functions that will match the regexs
		wg.Add(1)
		totalScan += 1
		curl <- input.Text()
	}
	wg.Wait()

	// Ending program
	close(results)
	close(curl)
	endTimestamp := time.Now().UnixNano()
	executionTime := calculateSeconds(StartTimestamp, endTimestamp)
	defer urlsFile.Close()
	fmt.Println("")
	log.Info().
		Msgf("Nipejs done: %d files with %d regex patterns scanned in %.2f seconds", Magenta(totalScan).Bold(), Cyan(allRegex).Bold(), Red(executionTime).Bold())
}

func matchRegex(target string, rlocation string, results chan Results, regexsfile io.Reader) {
	regexList := bufio.NewScanner(regexsfile)
	for regexList.Scan() {
		lineText := regexList.Text()
		lineText = strings.TrimSpace(lineText)
		if lineText == "" {
			continue
		}
		parts := strings.Split(lineText, "\t\t")
		regex := parts[0]
		category := ""
		if len(parts) > 1 {
			category = strings.Join(parts[1:], "\t\t")
		}
		nurex := pcre.MustCompile(regex)

		matches := nurex.FindAllString(target, -1)
		for _, match := range matches {
			wg.Add(1)
			results <- Results{match, rlocation, regex, category, float64(len(target)) / 1024}
		}
	}
}

func calculateSeconds(startTimestamp, endTimestamp int64) float64 {
	startTime := time.Unix(0, startTimestamp)
	endTime := time.Unix(0, endTimestamp)

	duration := endTime.Sub(startTime)

	return duration.Seconds()
}

func checkRegexs(file string) {
	regexFile, err := os.Open(file)
	if err != nil {
		log.Fatal().Msgf("Unable to open regex file: %v", err)
	}
	defer regexFile.Close()

	regexCategories := make(map[string]string)
	regexL := bufio.NewScanner(regexFile)
	line := 1
	for regexL.Scan() {
		lineText := regexL.Text()
		lineText = strings.TrimSpace(lineText)
		if lineText == "" {
			continue
		}

		parts := strings.Split(lineText, "\t\t")
		regex := parts[0]
		category := ""
		if len(parts) > 1 {
			category = strings.Join(parts[1:], "\t\t")
		}

		_, err := pcre.Compile(regex)
		if err != nil {
			log.Fatal().
				Msgf("Regex on line %d not valid: %v", Cyan(line).Bold(), Red(lineText).Bold())
		}

		regexCategories[regex] = category
		line++
	}

	if err := regexL.Err(); err != nil {
		log.Fatal().Msgf("Error reading regex file: %v", err)
	}
	for regexs, categories := range regexCategories {
		log.Debug().Msgf("Regex: %v,Category: %v", regexs, categories)
	}
}

/*
func checkRegexs(file string) {
	regexFile, err := os.Open(file)
	if err != nil {
		log.Fatal().Msg("Unable to open regex file")
	}
	defer regexFile.Close()

	regexCategories := make(map[string]string)
	regexL := bufio.NewScanner(regexFile)
	line := 1
	for regexL.Scan() {
		lineText := regexL.Text()
		parts := strings.SplitN(lineText, "\t", 2)

		if len(parts) > 0 {
			regex := parts[0]
			category := parts[1]
			if len(parts) > 1 {
				category = parts[1]
			}
			_, err := pcre.Compile(regex)
			if err != nil {
				log.Fatal().
					Msgf("Regex on line %d not valid: %v", Cyan(line).Bold(), Red(regexL.Text()).Bold())
			}
			regexCategories[regex] = category
		}

		line += 1
	}
	log.Debug().Msgf("%v", regexCategories)
}
*/
