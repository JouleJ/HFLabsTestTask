package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/PuerkitoBio/goquery"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/docs/v1"
	"google.golang.org/api/option"
	"jaytaylor.com/html2text"
	"log"
	"net/http"
	"os"
	"unicode/utf8"
)

const CONFLUENCE_URL = "https://confluence.hflabs.ru/pages/viewpage.action?pageId=1181220999"
const CREDENTIALS_PATH = "credentials.json"
const DOCUMENT_ID_PATH = "document_id.txt"
const TOKEN_PATH = "token.json"

type row struct {
	entries []string
}

type table struct {
	contents []row
}

func stripHtmlTags(s string) string {
	text, err := html2text.FromString(s, html2text.Options{PrettyTables: true})
	if err == nil {
		return text
	} else {
		return s
	}
}

func getTables() ([]table, error) {
	response, err := http.Get(CONFLUENCE_URL)
	if err != nil {
		return nil, err
	}

	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Non-okay status code: %v %v", response.StatusCode, response.Status)
	}

	document, err := goquery.NewDocumentFromReader(response.Body)
	if err != nil {
		return nil, err
	}

	tables := []table{}
	document.Find(".confluenceTable").Each(func(i int, tableSelection *goquery.Selection) {
		tableHtml, _ := tableSelection.Html()
		log.Println("TableHTML:", tableHtml)

		tbl := table{}
		tableSelection.Find("tr").Each(func(i int, rowSelection *goquery.Selection) {
			row := row{}
			rowSelection.Find("td, th").Each(func(i int, cellSelection *goquery.Selection) {
				html, _ := cellSelection.Html()
				row.entries = append(row.entries, stripHtmlTags(html))
			})

			tbl.contents = append(tbl.contents, row)
		})

		tables = append(tables, tbl)
	})

	return tables, nil
}

// Retrieves a token, saves the token, then returns the generated client.
func getClient(config *oauth2.Config) *http.Client {
	tok, err := tokenFromFile(TOKEN_PATH)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(TOKEN_PATH, tok)
	}
	return config.Client(context.Background(), tok)
}

// Requests a token from the web, then returns the retrieved token.
func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the "+
		"authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		log.Fatalf("Unable to read authorization code: %v", err)
	}

	tok, err := config.Exchange(oauth2.NoContext, authCode)
	if err != nil {
		log.Fatalf("Unable to retrieve token from web: %v", err)
	}
	return tok
}

// Retrieves a token from a local file.
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	defer f.Close()
	if err != nil {
		return nil, err
	}
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// Saves a token to a file path.
func saveToken(path string, token *oauth2.Token) {
	fmt.Printf("Saving credential file to: %s\n", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	defer f.Close()
	if err != nil {
		log.Fatalf("Unable to cache OAuth token: %v", err)
	}
	json.NewEncoder(f).Encode(token)
}

func getService() (*docs.Service, error) {
	ctx := context.Background()
	b, err := os.ReadFile(CREDENTIALS_PATH)
	if err != nil {
		return nil, fmt.Errorf("Unable to read client secret file: %v", err)
	}

	config, err := google.ConfigFromJSON(b, "https://www.googleapis.com/auth/documents")
	if err != nil {
		return nil, fmt.Errorf("Unable to parse client secret file to config: %v", err)
	}
	client := getClient(config)

	srv, err := docs.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("Unable to retrieve Docs client: %v", err)
	}

	return srv, nil
}

func getDocument(srv *docs.Service) (*docs.Document, error) {
	documentIdBytes, err := os.ReadFile(DOCUMENT_ID_PATH)
	if err == nil {
		return srv.Documents.Get(string(documentIdBytes)).Do()
	} else {
		doc, err := srv.Documents.Create(&docs.Document{Title: "HFLabsTestTaskTableDocument"}).Do()
		if err != nil {
			return nil, err
		}

		os.WriteFile(DOCUMENT_ID_PATH, []byte(doc.DocumentId), 0666)
		return doc, err
	}
}

func clearDocument(docId string, srv *docs.Service) error {
	doc, err := srv.Documents.Get(docId).Do()
	if err != nil {
		return err
	}

	bodyContentLength := len(doc.Body.Content)
	if bodyContentLength == 0 {
		return nil
	}

	startIndex := doc.Body.Content[0].StartIndex
	endIndex := doc.Body.Content[bodyContentLength-1].EndIndex
	log.Println(startIndex, endIndex)

	resp, err := srv.Documents.BatchUpdate(docId, &docs.BatchUpdateDocumentRequest{
		Requests: []*docs.Request{
			&docs.Request{
				DeleteContentRange: &docs.DeleteContentRangeRequest{
					Range: &docs.Range{
						StartIndex: startIndex + 1,
						EndIndex:   endIndex - 1,
					},
				},
			},
		},
	}).Do()

	log.Println("BatchUpdateResponse:", resp)
	if err != nil {
		return err
	}

	return nil
}

func insertTableToDocument(docId string, srv *docs.Service, tbl table) error {
	rowCnt := len(tbl.contents)
	if rowCnt == 0 {
		return fmt.Errorf("Empty table")
	}

	colCnt := len(tbl.contents[0].entries)
	for i := 0; i < rowCnt; i++ {
		if len(tbl.contents[i].entries) != colCnt {
			return fmt.Errorf("Invalid table: %v cells in first row, %v cell in row #%v", colCnt, len(tbl.contents[i].entries), i+1)
		}
	}

	resp, err := srv.Documents.BatchUpdate(docId, &docs.BatchUpdateDocumentRequest{
		Requests: []*docs.Request{
			&docs.Request{
				InsertTable: &docs.InsertTableRequest{
					Rows:                 int64(rowCnt),
					Columns:              int64(colCnt),
					EndOfSegmentLocation: &docs.EndOfSegmentLocation{},
				},
			},
		},
	}).Do()

	log.Println("BatchUpdateResponse:", resp)
	if err != nil {
		return err
	}

	doc, err := srv.Documents.Get(docId).Do()
	if err != nil {
		return err
	}

	bodyContentLength := len(doc.Body.Content)
	tableIdx := -1
	for i := 0; i < bodyContentLength; i++ {
		if doc.Body.Content[i].Table != nil {
			tableIdx = i
		}
	}

	if tableIdx == -1 {
		return fmt.Errorf("Failed to find last table in doc.Body.Content")
	}

	requests := []*docs.Request{}

	totalInserted := int64(0)
	for rowIdx, row := range doc.Body.Content[tableIdx].Table.TableRows {
		if row != nil {
			for cellIdx, cell := range row.TableCells {
				if cell != nil {
					text := tbl.contents[rowIdx].entries[cellIdx]
					requests = append(requests, &docs.Request{
						InsertText: &docs.InsertTextRequest{
							Text:     text,
							Location: &docs.Location{Index: cell.StartIndex + 1 + totalInserted},
						},
					})

					totalInserted += int64(utf8.RuneCountInString(text))
				}
			}
		}
	}

	resp, err = srv.Documents.BatchUpdate(docId, &docs.BatchUpdateDocumentRequest{
		Requests: requests,
	}).Do()

	log.Println("BatchUpdateResponse:", resp)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	tables, err := getTables()
	if err != nil {
		log.Fatalf("Failed to get tables: %v\n", err)
	}
	log.Println("TablesCount:", len(tables))

	srv, err := getService()
	if err != nil {
		log.Fatalf("Failed to get service: %v", err)
	}

	doc, err := getDocument(srv)
	if err != nil {
		log.Fatalf("Failed to get document: %v\n", err)
	}
	log.Println("DocumentId:", doc.DocumentId)

	err = clearDocument(doc.DocumentId, srv)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	}

	for _, tbl := range tables {
		err := insertTableToDocument(doc.DocumentId, srv, tbl)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	}
}
