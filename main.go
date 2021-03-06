package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	_ "net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/garyburd/go-oauth/oauth"
	"github.com/gorilla/securecookie"
	"github.com/kjk/u"
)

var (
	cookieName = "ckie"
)

var (
	oauthClient = oauth.Client{
		TemporaryCredentialRequestURI: "https://api.twitter.com/oauth/request_token",
		ResourceOwnerAuthorizationURI: "https://api.twitter.com/oauth/authorize",
		TokenRequestURI:               "https://api.twitter.com/oauth/access_token",
	}

	config = struct {
		TwitterOAuthCredentials *oauth.Credentials
		CookieAuthKeyHexStr     *string
		CookieEncrKeyHexStr     *string
		AnalyticsCode           *string
		AwsAccess               *string
		AwsSecret               *string
		S3BackupBucket          *string
		S3BackupDir             *string
	}{
		&oauthClient.Credentials,
		nil, nil,
		nil,
		nil, nil,
		nil, nil,
	}
	logger        *ServerLogger
	cookieAuthKey []byte
	cookieEncrKey []byte
	secureCookie  *securecookie.SecureCookie

	dataDir string

	store         *Store
	storeCrashes  *StoreCrashes
	alwaysLogTime = true
)

func StringEmpty(s *string) bool {
	return s == nil || 0 == len(*s)
}

func S3BackupEnabled() bool {
	if !inProduction {
		logger.Notice("s3 backups disabled because not in production")
		return false
	}
	if StringEmpty(config.AwsAccess) {
		logger.Notice("s3 backups disabled because AwsAccess not defined in config.json")
		return false
	}
	if StringEmpty(config.AwsSecret) {
		logger.Notice("s3 backups disabled because AwsSecret not defined in config.json")
		return false
	}
	if StringEmpty(config.S3BackupBucket) {
		logger.Notice("s3 backups disabled because S3BackupBucket not defined in config.json")
		return false
	}
	if StringEmpty(config.S3BackupDir) {
		logger.Notice("s3 backups disabled because S3BackupDir not defined in config.json")
		return false
	}
	return true
}

func getDataDir() string {
	if dataDir != "" {
		return dataDir
	}

	// on the server, must be done first because ExpandTildeInPath()
	// doesn't work when cross-compiled on mac for linux
	serverDir := filepath.Join("..", "..", "data")
	dataDir = serverDir
	if u.PathExists(dataDir) {
		return dataDir
	}

	// locally
	localDir := u.ExpandTildeInPath("~/data/blog")
	dataDir = localDir
	if u.PathExists(dataDir) {
		return dataDir
	}

	log.Fatalf("data directory (%q or %q) doesn't exist", serverDir, localDir)
	return ""
}

func isTopLevelUrl(url string) bool {
	return 0 == len(url) || "/" == url
}

func getReferer(r *http.Request) string {
	return r.Header.Get("Referer")
}

// this list was determined by watching /logs
var noLog404 = map[string]bool{
	"/crossdomain.xml":                                               true,
	"/article/Exercise-links-1.html":                                 true,
	"/article/Ecco-for-free.html":                                    true,
	"/article/Disappointed-by-The-Bat.html":                          true,
	"/article/Comments-need-not-apply.html":                          true,
	"/article/Browsing-Newton.html":                                  true,
	"/article/Perl-and-lisp-programmers.html":                        true,
	"/article/iPod-competition.html":                                 true,
	"/article/Programming-Jabber.html":                               true,
	"/article/Good-software-design-contradicts-eXtreme-Program.html": true,
	"/article/Bloglines-vs-Google-Reader-the-verdict.html":           true,
	"/2002/07/30/stuid-coding-mistake-of-the-day.html":               true,
	"/article/Corman-Lisp.html":                                      true,
	"/article/Offshore-outsourcing.html":                             true,
	"/article/Nabble-hosted-forums.html":                             true,
}

func shouldLog404(s string) bool {
	if strings.HasPrefix(s, "/apple-touch-icon") {
		return false
	}
	_, ok := noLog404[s]
	return !ok
}

func userIsAdmin(cookie *SecureCookieValue) bool {
	return cookie.TwitterUser == "kjk"
}

// reads the configuration file from the path specified by
// the config command line flag.
func readConfig(configFile string) error {
	b, err := ioutil.ReadFile(configFile)
	if err != nil {
		return err
	}
	err = json.Unmarshal(b, &config)
	if err != nil {
		return err
	}
	cookieAuthKey, err = hex.DecodeString(*config.CookieAuthKeyHexStr)
	if err != nil {
		return err
	}
	cookieEncrKey, err = hex.DecodeString(*config.CookieEncrKeyHexStr)
	if err != nil {
		return err
	}
	secureCookie = securecookie.New(cookieAuthKey, cookieEncrKey)
	// verify auth/encr keys are correct
	val := map[string]string{
		"foo": "bar",
	}
	_, err = secureCookie.Encode(cookieName, val)
	if err != nil {
		// for convenience, if the auth/encr keys are not set,
		// generate valid, random value for them
		auth := securecookie.GenerateRandomKey(32)
		encr := securecookie.GenerateRandomKey(32)
		fmt.Printf("auth: %s\nencr: %s\n", hex.EncodeToString(auth), hex.EncodeToString(encr))
	}
	// TODO: somehow verify twitter creds
	return err
}

// Request.RemoteAddress contains port, which we want to remove i.e.:
// "[::1]:58292" => "[::1]"
func ipAddrFromRemoteAddr(s string) string {
	idx := strings.LastIndex(s, ":")
	if idx == -1 {
		return s
	}
	return s[:idx]
}

func getIpAddress(r *http.Request) string {
	hdr := r.Header
	hdrRealIp := hdr.Get("X-Real-Ip")
	hdrForwardedFor := hdr.Get("X-Forwarded-For")
	if hdrRealIp == "" && hdrForwardedFor == "" {
		return ipAddrFromRemoteAddr(r.RemoteAddr)
	}
	if hdrForwardedFor != "" {
		// X-Forwarded-For is potentially a list of addresses separated with ","
		parts := strings.Split(hdrForwardedFor, ",")
		for i, p := range parts {
			parts[i] = strings.TrimSpace(p)
		}
		// TODO: should return first non-local address
		return parts[0]
	}
	return hdrRealIp
}

func jQueryUrl() string {
	//return "/js/jquery-1.4.2.js"
	return "//cdnjs.cloudflare.com/ajax/libs/jquery/1.8.3/jquery.min.js"
}

func highlightJsUrl() string {
	return "//cdnjs.cloudflare.com/ajax/libs/highlight.js/8.4/highlight.min.js"
}

func highlightCssUrl() string {
	return "https://cdnjs.cloudflare.com/ajax/libs/highlight.js/8.4/styles/default.min.css"
}

func makeTimingHandler(fn func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		metricCurrentReqs.Inc(1)
		defer metricCurrentReqs.Dec(1)
		startTime := time.Now()
		fn(w, r)
		duration := time.Now().Sub(startTime)
		// log urls that take long time to generate i.e. over 1 sec in production
		// or over 0.1 sec in dev
		shouldLog := duration.Seconds() > 1.0
		if alwaysLogTime && duration.Seconds() > 0.1 {
			shouldLog = true
		}
		if shouldLog {
			url := r.URL.Path
			if len(r.URL.RawQuery) > 0 {
				url = fmt.Sprintf("%s?%s", url, r.URL.RawQuery)
			}
			logger.Noticef("%q took %f seconds to serve", url, duration.Seconds())
		}
		// TODO: add query to url
		metricHttpReqRate.Mark(1)
		metricHttpReqTime.Update(duration)
		LogSlowPage(r.URL.Path, duration)
	}
}

func setContentType(w http.ResponseWriter, contentType string) {
	w.Header().Set("Content-Type", contentType)
}

func writeResponse(w http.ResponseWriter, responseBody string) {
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(responseBody)), 10))
	io.WriteString(w, responseBody)
}

func textResponse(w http.ResponseWriter, text string) {
	setContentType(w, "text/plain")
	writeResponse(w, text)
}

var emptyString = ""

var test = []byte(`Crashed thread:
0114C072 01:0004B072 sumatrapdf.exe!CrashMe+0x2 c:\users\kkowalczyk\src\sumatrapdf\src\utils\baseutil.cpp+14
0112F0AD 01:0002E0AD sumatrapdf.exe!PrintToDevice+0x1d c:\users\kkowalczyk\src\sumatrapdf\src\print.cpp+111
011303E2 01:0002F3E2 sumatrapdf.exe!PrintThreadData::PrintThread+0x42 c:\users\kkowalczyk\src\sumatrapdf\src\print.cpp+420
76031114 01:00050114 kernel32.dll!BaseThreadInitThunk+0x12
7757B299 01:0005A299 ntdll.dll!RtlInitializeExceptionChain+0x63
7757B26C 01:0005A26C ntdll.dll!RtlInitializeExceptionChain+0x36`)

var (
	configPath      string
	httpAddr        string
	inProduction    bool
	newArticleTitle string
)

func parseCmdLineArgs() {
	flag.StringVar(&configPath, "config", "config.json", "Path to configuration file")
	flag.StringVar(&httpAddr, "addr", ":5020", "HTTP server address")
	flag.BoolVar(&inProduction, "production", false, "are we running in production")
	flag.StringVar(&newArticleTitle, "newarticle", "", "create a new article")
	flag.Parse()
}

func isTmpFile(path string) bool {
	return strings.HasSuffix(path, ".tmp")
}

func sanitizeForFile(s string) string {
	var res []byte
	toRemove := "/\\#()[]{},?+.'\""
	var prev rune
	buf := make([]byte, 3)
	for _, c := range s {
		if strings.ContainsRune(toRemove, c) {
			continue
		}
		switch c {
		case ' ', '_':
			c = '-'
		}
		if c == prev {
			continue
		}
		prev = c
		n := utf8.EncodeRune(buf, c)
		for i := 0; i < n; i++ {
			res = append(res, buf[i])
		}
	}
	if len(res) > 32 {
		res = res[:32]
	}
	s = string(res)
	s = strings.Trim(s, "_- ")
	s = strings.ToLower(s)
	return s
}

func findUniqueArticleId(articles []*Article) int {
	ids := make([]int, 0)
	for _, a := range articles {
		ids = append(ids, a.Id)
	}
	if len(ids) == 0 {
		return 1
	}
	sort.Ints(ids)
	prevId := ids[0]
	for i := 1; i < len(ids); i++ {
		if ids[i] != prevId+1 {
			return prevId + 1
		}
		prevId = ids[i]
	}
	return prevId + 1
}

func genNewArticle(title string) {
	fmt.Printf("genNewArticle: %q\n", title)
	store, err := NewStore()
	if err != nil {
		log.Fatalf("NewStore() failed with %s", err)
	}
	newId := findUniqueArticleId(store.articles)
	name := sanitizeForFile(title) + ".md"
	fmt.Printf("new id: %d, name: %s\n", newId, name)
	t := time.Now()
	dir := "blog_posts"
	d := t.Format("2006-01")
	path := filepath.Join(dir, d, name)
	s := fmt.Sprintf(`Id: %d
Title: %s
Date: %s
Format: Markdown
--------------`, newId, title, t.Format(time.RFC3339))
	for i := 1; i < 10; i++ {
		if !u.PathExists(path) {
			break
		}
		name := sanitizeForFile(title) + "-" + strconv.Itoa(i) + ".md"
		path = filepath.Join(dir, d, name)
	}
	u.PanicIf(u.PathExists(path))
	fmt.Printf("path: %s\n", path)
	u.CreateDirForFileMust(path)
	ioutil.WriteFile(path, []byte(s), 0644)
}

func main() {
	var err error

	runtime.GOMAXPROCS(runtime.NumCPU())
	parseCmdLineArgs()

	if newArticleTitle != "" {
		genNewArticle(newArticleTitle)
		return
	}

	/*findFileFixes("../../../sumatrapdf")
	return
	s := linkifyCrashReport(test)
	fmt.Print(string(s))
	return*/

	if inProduction {
		reloadTemplates = false
		alwaysLogTime = false
	}

	useStdout := !inProduction
	logger = NewServerLogger(256, 256, useStdout)

	rand.Seed(time.Now().UnixNano())

	if err := readConfig(configPath); err != nil {
		log.Fatalf("Failed reading config file %s. %s\n", configPath, err)
	}

	if !inProduction {
		config.AnalyticsCode = &emptyString
	}

	if store, err = NewStore(); err != nil {
		log.Fatalf("NewStore() failed with %s", err)
	}
	buildArticlesCache()

	if storeCrashes, err = NewStoreCrashes(getDataDir()); err != nil {
		log.Fatalf("NewStoreCrashes() failed with %s", err)
	}

	readRedirects()
	InitMetrics()

	backupConfig := &BackupConfig{
		AwsAccess: *config.AwsAccess,
		AwsSecret: *config.AwsSecret,
		Bucket:    *config.S3BackupBucket,
		S3Dir:     *config.S3BackupDir,
		LocalDir:  getDataDir(),
	}

	if S3BackupEnabled() {
		go BackupLoop(backupConfig)
	}

	startWatching()
	InitHttpHandlers()
	logger.Noticef(fmt.Sprintf("Started runing on %s", httpAddr))
	if err := http.ListenAndServe(httpAddr, nil); err != nil {
		fmt.Printf("http.ListendAndServer() failed with %s\n", err)
	}
	fmt.Printf("Exited\n")
}
