package main

import (
	"html/template"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	NOTE_TAG = "note"
)

func getTrimmedFormValue(r *http.Request, name string) string {
	return strings.TrimSpace(r.FormValue(name))
}

func formatNameToId(name string) int {
	switch name {
	case "html":
		return FormatHtml
	case "textile":
		return FormatTextile
	case "markdown":
		return FormatMarkdown
	case "text":
		return FormatText
	}
	return FormatUnknown
}

// url: /app/preview
func handleAppPreview(w http.ResponseWriter, r *http.Request) {
	format := getTrimmedFormValue(r, "format")
	formatInt := formatNameToId(format)
	// TODO: what to do on error?
	msg := getTrimmedFormValue(r, "note")
	s := msgToHtml([]byte(msg), formatInt)
	//w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(s))
}

func checkboxToBool(checkboxVal string) bool {
	return "on" == checkboxVal;
}

func tagsFromString(s string) []string {
	tags := strings.Split(s, ",")
	for i, tag := range tags {
		tags[i] = strings.TrimSpace(tag)
	}
	return tags
}

// url: POST /app/edit
func createNewOrUpdatePost(w http.ResponseWriter, r *http.Request, article *Article) {
	format := formatNameToId(getTrimmedFormValue(r, "format"))
	if !validFormat(format) {
		serveErrorMsg(w, "invalid format")
		return
	}
	title := getTrimmedFormValue(r, "title")
	if title == "" {
		serveErrorMsg(w, "empty title not valid")
		return
	}
	body := getTrimmedFormValue(r, "note")
	if len(body) < 10 {
		serveErrorMsg(w, "body too small")
		return
	}
	isPrivate := checkboxToBool(getTrimmedFormValue(r, "private_checkbox"))
	tags := tagsFromString(getTrimmedFormValue(r, "tags"))

	text, err := store.CreateNewText(format, body)
	if err != nil {
		logger.Errorf("createNewOrUpdatePost(): store.CreateNewText() failed with %s", err.Error())
		serveErrorMsg(w, "error creating text")
		return
	}
	if article == nil {
		article = &Article{
			Id: 0,
			PublishedOn: time.Now(),
			Versions: make([]*Text, 0),
		}
	}
	article.Versions = append(article.Versions, text)
	updatePublishedOn := checkboxToBool(getTrimmedFormValue(r, "update_published_on"))
	if updatePublishedOn {
		article.PublishedOn = time.Now()
	}
	article.Title = title
	article.IsPrivate = isPrivate
	article.IsDeleted = false
	article.Tags = tags
	if article, err = store.CreateOrUpdateArticle(article); err != nil {
		logger.Errorf("createNewOrUpdatePost(): store.CreateNewArticle() failed with %s", err.Error())
		serveErrorMsg(w, "error creating article")
		return
	}
	clearArticlesCache()
	url := "/" + article.Permalink()
	http.Redirect(w, r, url, 301)
}

func GetArticleVersionBody(sha1 []byte) (string, error) {
	msgFilePath := store.MessageFilePath(sha1)
	msg, err := ioutil.ReadFile(msgFilePath)
	if err != nil {
		return "", err
	}
	return string(msg), nil
}

// url: /app/edit
func handleAppEdit(w http.ResponseWriter, r *http.Request) {
	if !IsAdmin(r) {
		serve404(w, r)
		return
	}

	tags := make([]string, 0)
	if getTrimmedFormValue(r, "note") == "yes" {
		tags = append(tags, NOTE_TAG)
	}

	var article *Article
	articleIdStr := getTrimmedFormValue(r, "article_id")
	if articleId, err := strconv.Atoi(articleIdStr); err == nil {
		article = store.GetArticleById(articleId)
	}

	if r.Method == "POST" {
		createNewOrUpdatePost(w, r, article)
		return
	}

	model := struct {
		PrettifyCssUrl         string
		PrettifyJsUrl          string
		JqueryUrl              string
		FormatTextileChecked   string
		FormatMarkdownChecked  string
		FormatHtmlChecked      string
		FormatTextChecked      string
		PrivateCheckboxChecked string
		SubmitButtonText       string
		ArticleId              int
		ArticleTitle           string
		ArticleBody            template.HTML
		Tags                   string
	}{
		JqueryUrl:      jQueryUrl(),
		PrettifyJsUrl:  prettifyJsUrl(),
		PrettifyCssUrl: prettifyCssUrl(),
	}

	if article == nil {
		model.FormatTextileChecked = "selected"
		model.PrivateCheckboxChecked = "checked"
		model.SubmitButtonText = "Post"
		model.Tags = strings.Join(tags, ",")
	} else {
		model.ArticleId = article.Id
		model.ArticleTitle = article.Title
		ver := article.CurrVersion()
		if body, err := GetArticleVersionBody(ver.Sha1[:]); err != nil {
			panic("GetArticleVersionBody() failed")
		} else {
			model.ArticleBody = template.HTML(body)
		}
		model.SubmitButtonText = "Update post"
		model.Tags = strings.Join(article.Tags, ",")
		if article.IsPrivate {
			model.PrivateCheckboxChecked = "checked"
			format := article.CurrVersion().Format
			checked := &model.FormatTextChecked
			if format == FormatHtml {
				checked = &model.FormatHtmlChecked
			} else if format == FormatTextile {
				checked = &model.FormatTextileChecked
			} else if format == FormatMarkdown {
				checked = &model.FormatMarkdownChecked
			} else if format != FormatText {
				panic("invalid format")
			}
			*checked = "selected"
		}
	}

	ExecTemplate(w, tmplEdit, model)
}