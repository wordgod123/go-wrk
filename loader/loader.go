package loader

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"regexp"
	"gopkg.in/yaml.v2"
	// "github.com/valyala/fastjson"
	"github.com/robertkrimen/otto"
	"github.com/wordgod123/go-wrk/util"
)

const (
	USER_AGENT = "go-wrk"
)

type PipelineRequest struct {
	Url string
	Parameters string
	Method string
	Response string
}

type LoadCfg struct {
	duration           int //seconds
	goroutines         int
	testUrl            string
	reqBody            string
	method             string
	host               string
	header             map[string]string
	statsAggregator    chan *RequesterStats
	timeoutms          int
	allowRedirects     bool
	disableCompression bool
	disableKeepAlive   bool
	interrupted        int32
	clientCert         string
	clientKey          string
	caCert             string
	http2              bool
	requestfile		   string
}

// RequesterStats used for colelcting aggregate statistics
type RequesterStats struct {
	TotRespSize    int64
	TotDuration    time.Duration
	MinRequestTime time.Duration
	MaxRequestTime time.Duration
	NumRequests    int
	NumErrs        int
	ResponseNotMatch int
	TemplateRenderTime time.Duration
	MaxTempRenderTime time.Duration
	MinTempRenderTime time.Duration
}

func NewLoadCfg(duration int, //seconds
	goroutines int,
	testUrl string,
	reqBody string,
	method string,
	host string,
	header map[string]string,
	statsAggregator chan *RequesterStats,
	timeoutms int,
	allowRedirects bool,
	disableCompression bool,
	disableKeepAlive bool,
	clientCert string,
	clientKey string,
	caCert string,
	http2 bool,
	requestfile string) (rt *LoadCfg) {
	rt = &LoadCfg{duration, goroutines, testUrl, reqBody, method, host, header, statsAggregator, timeoutms,
		allowRedirects, disableCompression, disableKeepAlive, 0, clientCert, clientKey, caCert, http2, requestfile}
	return
}

func escapeUrlStr(in string) string {
	qm := strings.Index(in, "?")
	if qm != -1 {
		qry := in[qm+1:]
		qrys := strings.Split(qry, "&")
		var query string = ""
		var qEscaped string = ""
		var first bool = true
		for _, q := range qrys {
			qSplit := strings.Split(q, "=")
			if len(qSplit) == 2 {
				qEscaped = qSplit[0] + "=" + url.QueryEscape(qSplit[1])
			} else {
				qEscaped = qSplit[0]
			}
			if first {
				first = false
			} else {
				query += "&"
			}
			query += qEscaped

		}
		return in[:qm] + "?" + query
	} else {
		return in
	}
}

//DoRequest single request implementation. Returns the size of the response and its duration
//On error - returns -1 on both
func DoRequest(httpClient *http.Client, header map[string]string, method, host, loadUrl, reqBody string) (respSize int, duration time.Duration, body []byte) {
	respSize = -1
	duration = -1

	loadUrl = escapeUrlStr(loadUrl)

	var buf io.Reader
	if len(reqBody) > 0 {
		buf = bytes.NewBufferString(reqBody)
	}

	req, err := http.NewRequest(method, loadUrl, buf)
	if err != nil {
		fmt.Println("An error occured doing request", err)
		return
	}

	for hk, hv := range header {
		req.Header.Add(hk, hv)
	}

	req.Header.Add("User-Agent", USER_AGENT)
	if host != "" {
		req.Host = host
	}
	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		fmt.Println("redirect?")
		//this is a bit weird. When redirection is prevented, a url.Error is retuned. This creates an issue to distinguish
		//between an invalid URL that was provided and and redirection error.
		rr, ok := err.(*url.Error)
		if !ok {
			fmt.Println("An error occured doing request", err, rr)
			return
		}
		fmt.Println("An error occured doing request", err)
	}
	if resp == nil {
		fmt.Println("empty response")
		return
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
	}()
	body, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("An error occured reading body", err)
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		duration = time.Since(start)
		respSize = len(body) + int(util.EstimateHttpHeadersSize(resp.Header))
	} else if resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusTemporaryRedirect {
		duration = time.Since(start)
		respSize = int(resp.ContentLength) + int(util.EstimateHttpHeadersSize(resp.Header))
	} else {
		fmt.Println("received status code", resp.StatusCode, "from", resp.Header, "content", string(body), req)
	}

	return
}

//Requester a go function for repeatedly making requests and aggregating statistics as long as required
//When it is done, it sends the results using the statsAggregator channel
func (cfg *LoadCfg) RunSingleLoadSession() {
	stats := &RequesterStats{MinRequestTime: time.Minute}
	start := time.Now()

	httpClient, err := client(cfg.disableCompression, cfg.disableKeepAlive, cfg.timeoutms, cfg.allowRedirects, cfg.clientCert, cfg.clientKey, cfg.caCert, cfg.http2)
	if err != nil {
		log.Fatal(err)
	}

	for time.Since(start).Seconds() <= float64(cfg.duration) && atomic.LoadInt32(&cfg.interrupted) == 0 {
		respSize, reqDur, body := DoRequest(httpClient, cfg.header, cfg.method, cfg.host, cfg.testUrl, cfg.reqBody)
		fmt.Println(body)
		if respSize > 0 {
			stats.TotRespSize += int64(respSize)
			stats.TotDuration += reqDur
			stats.MaxRequestTime = util.MaxDuration(reqDur, stats.MaxRequestTime)
			stats.MinRequestTime = util.MinDuration(reqDur, stats.MinRequestTime)
			stats.NumRequests++
		} else {
			stats.NumErrs++
		}
	}
	cfg.statsAggregator <- stats
}

//Requester a go function for repeatedly making requests and aggregating statistics as long as required
//When it is done, it sends the results using the statsAggregator channel
//banya presure test
func (cfg *LoadCfg) RunSingleLoadSessionBy() {
	stats := &RequesterStats{MinRequestTime: time.Minute}
	start := time.Now()

	httpClient, err := client(cfg.disableCompression, cfg.disableKeepAlive, cfg.timeoutms, cfg.allowRedirects, cfg.clientCert, cfg.clientKey, cfg.caCert, cfg.http2)
	if err != nil {
		log.Fatal(err)
	}

	requestfile := cfg.requestfile
	requestData, err := ioutil.ReadFile(requestfile)
	// fmt.Println(string(requestData))
	// requestDataStr = string(requestData)

	requestParams := []PipelineRequest{}
	err = yaml.Unmarshal(requestData, &requestParams)
	if err != nil {
		fmt.Println("yaml decode has error , ", err)
	}
	totalRequestsLen := len(requestParams)

	// var Responses = make([]*fastjson.Value, totalRequestsLen)
	var Responses = make([]string, totalRequestsLen)
	vm := otto.New()

	templateCompiler := regexp.MustCompile("{{([^{}]+)}}")

	// var fastjsonParser fastjson.Parser

	// fmt.Println(requestParams)

	for time.Since(start).Seconds() <= float64(cfg.duration) && atomic.LoadInt32(&cfg.interrupted) == 0 {
		for index, request := range requestParams{
			url := request.Url
			method := request.Method
			paras := request.Parameters
			response := request.Response
			// fmt.Println("BODY: ", paras)

			now := time.Now()

			vm.Set("item1", Responses)

			if strings.Index( paras, "{{") >= 0 {
				r := templateCompiler.FindAllStringSubmatch(paras, -1)
				// fmt.Println(r, len(r))
				for _, iTemplate := range r {
					if len(iTemplate) == 2{
						vm.Run(fmt.Sprintf("item=[];for(i in item1){if(item1[i] != ''){item[i] = JSON.parse(item1[i]);} }; v = %s", iTemplate[1]))
						// vm.Run(fmt.Sprintf("for(i in item){console.log(i)};console.log(JSON.parse(item[0]).version); v = %s", iTemplate[1]))

						itemValue, err := vm.Get("v")
						// fmt.Println(iTemplate, itemValue)
						if err != nil {
							fmt.Printf("get template value failed, %s", err)
						}
						itemResult, err := itemValue.ToString()
						oldtemplate := fmt.Sprintf("{{%s}}", iTemplate[1])
						// fmt.Println(oldtemplate, itemResult)
						paras = strings.Replace(paras, oldtemplate, itemResult, -1)
					}
				}
			}

			templateRenderTime := time.Since(now)
		
			// respSize, reqDur, body := DoRequest(httpClient, cfg.header, cfg.method, cfg.host, cfg.testUrl, cfg.reqBody)
			// fmt.Printf("method: %s, host: %s, url: %s, params: %s, response: %s", method, "", url, paras, response)
			// fmt.Println(paras)
			respSize, reqDur, body := DoRequest(httpClient, cfg.header, method, "", url, paras)
			// fmt.Println(body)
			if respSize > 0 {
				stats.TotRespSize += int64(respSize)
				stats.TotDuration += reqDur
				stats.MaxRequestTime = util.MaxDuration(reqDur, stats.MaxRequestTime)
				stats.MinRequestTime = util.MinDuration(reqDur, stats.MinRequestTime)
				stats.NumRequests++
				stats.TemplateRenderTime += templateRenderTime
				
				// responseJson, err := fastjsonParser.ParseBytes(body)
				// if err != nil {
				// 	fmt.Println("json parse failed, ", err)
				// }
				// Responses[index] = responseJson //将response写入 Response中，便于后面请求中使用当中的值

				Responses[index] = string(body)

				if response != string(body) {
					// fmt.Printf("response is not matched, need: %s, result is : %s", response, string(body))
					stats.ResponseNotMatch += 1
				}

			} else {
				stats.NumErrs++
			}

		}
	}
	cfg.statsAggregator <- stats
}

func (cfg *LoadCfg) Stop() {
	atomic.StoreInt32(&cfg.interrupted, 1)
}