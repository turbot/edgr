package core

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/piquette/edgr/core/model"
	"golang.org/x/net/html/charset"
)

var (
	iexSymbolsURL = "https://api.iex.cloud/v1/data/core/REF_DATA/symbols"
	secCompanyURL = "https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=%s&start=0&count=1&output=atom"
	iexCompanyURL = "https://api.iextrading.com/1.0/stock/spy/company"
	dirRegex      = regexp.MustCompile(`<td><a href="(.*?)"><img`)
	urlRegex      = regexp.MustCompile(`.*<a href="(.*?)index.html"><img`)
)

// Filer models
// -----------------

// Company is a simple struct for a single company.
type Company struct {
	CIK                 string `json:"cik"`
	Currency            string `json:"currency"`
	Date                string `json:"date"`
	Exchange            string `json:"exchange"`
	ExchangeName        string `json:"exchangeName"`
	ExchangeSegment     string `json:"exchangeSegment"`
	ExchangeSegmentName string `json:"exchangeSegmentName"`
	ExchangeSuffix      string `json:"exchangeSuffix"`
	FIGI                string `json:"figi"`
	IEXID               string `json:"iexId"`
	IsEnabled           bool   `json:"isEnabled"`
	LEI                 string `json:"lei"`
	Name                string `json:"name"`
	Region              string `json:"region"`
	Symbol              string `json:"symbol"`
	Type                string `json:"type"`
}

// rssFeed is the feed obj.
type rssFeed struct {
	Info secFilerInfo `xml:"company-info"`
}

type secFilerInfo struct {
	CIK     string `xml:"cik"`
	SIC     string `xml:"assigned-sic,omitempty"`
	SICDesc string `xml:"assigned-sic-desc,omitempty"`
	Name    string `xml:"conformed-name"`
}

// Filer methods
// -----------------

// GetPublicCompanies returns a list of public companies.
func GetPublicCompanies() ([]Company, error) {
	return GetPublicCompaniesWithHeaders(map[string]string{}, map[string]string{})
}

// GetPublicCompanies returns a list of public companies.
// The query parameter should include a key 'token' with a value for authentication purposes.
func GetPublicCompaniesWithHeaders(queryParameters map[string]string, headers map[string]string) ([]Company, error) {

	if queryParameters["token"] == "" {
		return nil, fmt.Errorf("To access the endpoint at https://api.iex.cloud, you must include the 'token' in the query parameters.")
	}

	client := &http.Client{Timeout: 10 * time.Second}

	parameters := url.Values{}
	for k, v := range queryParameters {
		parameters.Add(k, v)
	}

	req, err := http.NewRequest("GET", iexSymbolsURL + "?" + parameters.Encode(), nil)
	if err != nil {
		return []Company{}, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	var result []Company

	resp, err := client.Do(req)
	if err != nil {
		return []Company{}, err
	}
	defer resp.Body.Close()

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading response body:", err)
		return nil, fmt.Errorf("error reading response body: %s", err)
	}
	err = json.Unmarshal(body, &result)
	if err != nil {
		fmt.Println("Error unmarshaling JSON:", err)
		return nil, fmt.Errorf("error unmarshaling JSON: %s", string(body))
	}
	return result, nil
}

// GetFiler gets a single filer from the SEC website based on symbol.
func GetFiler(symbol string) (filer *model.Filer, err error) {
	return GetFilerWithHeaders(symbol, map[string]string{})
}

// GetFilerWithHeaders gets a single filer from the SEC website based on symbol, adding the given HTTP headers to the request.
func GetFilerWithHeaders(symbol string, headers map[string]string) (filer *model.Filer, err error) {

	// get the cik for each symbol.
	// tedious process...
	url := fmt.Sprintf(secCompanyURL, symbol)
	client := &http.Client{Timeout: 10 * time.Second}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var feed rssFeed
	decoder := xml.NewDecoder(resp.Body)
	decoder.CharsetReader = charset.NewReaderLabel
	err = decoder.Decode(&feed)
	if err != nil {
		return
	}
	if feed.Info.CIK == "" {
		err = fmt.Errorf("no cik found in response data")
		return
	}
	if feed.Info.Name == "" {
		err = fmt.Errorf("no name found in response data")
		return
	}
	return &model.Filer{
		CIK:            feed.Info.CIK,
		Symbol:         symbol,
		SIC:            feed.Info.SIC,
		SICDescription: feed.Info.SICDesc,
		Name:           feed.Info.Name,
	}, nil
}

// Filings models
// -----------------

// SECFiling contains a single instance of an sec filing.
type SECFiling struct {
	Filing *model.Filing
	Docs   []*model.Document
}

// Filings methods
// -----------------

// GetFilings gets a list of filings for a single CIK.
func GetFilings(cik, formtype, stoptime string) (filings []SECFiling, err error) {

	var stop *time.Time
	if stoptime != "" {
		t, err := time.Parse("2006-01-02", stoptime)
		if err != nil {
			return filings, err
		}
		stop = &t
	}

	dirPage, err := getPage("https://www.sec.gov/Archives/edgar/data/"+cik, 2)
	if err != nil {
		return
	}

	urls := findListURLs(dirPage)

	for _, u := range urls {
		docsPage, getErr := getPage(u, 2)
		if getErr != nil {
			log.Println("couldnt find page:", getErr)
			continue
		}

		idxURL := findIdxURL(docsPage)
		if idxURL == "" {
			log.Println("couldnt regex idx url")
			continue
		}

		filing, buildErr := buildFiling(cik, idxURL)
		if buildErr != nil {
			log.Println(buildErr)
			continue
		}
		if formtype != "" {
			// check form type.
			if filing.Filing.FormType != formtype {
				continue
			}
		}

		if stop != nil {
			// check cutoff time.
			if filing.Filing.EdgarTime.Before(*stop) {
				return
			}
		}
		// Do stuff with the filing...
		filing.Filing.AllSymbols = []string{filing.Filing.Symbol}
		fmt.Println(filing)
		filings = append(filings, filing)
	}

	return
}
