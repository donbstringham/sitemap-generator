package sitemapgen

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/eapache/channels"
	"github.com/maciekmm/sitemap-generator/config"
	"github.com/maciekmm/sitemap-generator/filegen"
	"github.com/maciekmm/sitemap-generator/limit"
	"github.com/temoto/robotstxt-go"
)

type SitemapGenerator struct {
	WorkerQueue *channels.InfiniteChannel
	waitGroup   *sync.WaitGroup
	config      *config.Config
}

//NewSitemapGenerator constructs a new sitemap generator instance,
//Call Start() in order to start the proccesszz
func NewSitemapGenerator(config *config.Config) *SitemapGenerator {
	return &SitemapGenerator{channels.NewInfiniteChannel(), new(sync.WaitGroup), config}
}

//Start gives the whole machine a spin
//TODO: Divide and conquer :>
func (sg *SitemapGenerator) Start() error {
	parsed, err := url.Parse(sg.config.URL)
	if err != nil {
		return err
	}

	//Parse robots.txt
	var robs *robotstxt.RobotsData
	if sg.config.Parsing.RespectRobots {
		robs, err = GetRobots(parsed)
		if err != nil {
			log.Println(err.Error())
		}
	}

	//Create sitemapgenerator
	sitemapgen, err := filegen.NewGenerator(*sg.config, sg.waitGroup)
	if err != nil {
		log.Println("Dispatcher: " + err.Error())
		return err
	}
	go sitemapgen.Start()

	//Create validator
	log.Println("Dispatcher: Creating validator.")
	validator := NewValidator(*sg.config, sg.WorkerQueue, sg.waitGroup, robs, sitemapgen.Input)
	go validator.start()
	sg.waitGroup.Add(1)
	validator.Input <- parsed

	//Create proxies
	var httpCls []*limit.Client
	// cr := func(req *http.Request, via []*http.Request) error {
	// 	req.Header.Add("User-Agent", sg.config.Parsing.UserAgent)	//Construct the channel

	// 	if len(via) >= 20 {
	// 		return errors.New("stopped after 10 redirects")
	// 	}
	// 	return nil
	// }
	if sg.config.Parsing.NoProxyClient {
		client := &http.Client{}
		httpCls = append(httpCls, limit.NewClient(client, limit.NewRateLimiter(sg.config.Parsing.RequestsPerSecond, sg.config.Parsing.Burst), sg.config.Parsing.UserAgent))
	}

	for _, proxy := range sg.config.Parsing.Proxies {
		proxyURL, err := url.Parse(proxy.Address)
		if err != nil {
			log.Println("Dispatcher: Invalid proxy url: ", proxy.Address)
		}
		proxyURL.User = url.UserPassword(proxy.Username, proxy.Password)
		client := &http.Client{
			Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
			Timeout:   time.Duration(5 * time.Second),
		}
		httpCls = append(httpCls, limit.NewClient(client, limit.NewRateLimiter(sg.config.Parsing.RequestsPerSecond, sg.config.Parsing.Burst), sg.config.Parsing.UserAgent))
	}
	//Construct the channel
	log.Println("Dispatcher: Finished creating proxies, total: ", len(httpCls))
	httpClients := make(chan *limit.Client, len(httpCls))
	for _, cli := range httpCls {
		httpClients <- cli
	}

	//Create workers
	for i := 0; i < sg.config.Parsing.Workers; i++ {
		log.Println("Dispatcher: Creating worker no. ", i)
		worker := NewWorker(sg.WorkerQueue, validator.Input, sg.waitGroup, sitemapgen.Input, httpClients)
		go worker.Start()
	}

	//Wait for work to finish
	sg.waitGroup.Wait()
	log.Println("Dispatcher: All work's done, closing channels.")
	sg.WorkerQueue.Close()
	close(httpClients)
	close(validator.Input)
	close(sitemapgen.Input)
	//Sitemap generator cleanup
	sg.waitGroup.Add(1)
	sg.waitGroup.Wait()
	return nil
}

//GetRobots gets RobotsData for given url
func GetRobots(url *url.URL) (*robotstxt.RobotsData, error) {
	resp, err := http.DefaultClient.Get("http://" + url.Host + "/robots.txt")
	if err != nil {
		return nil, fmt.Errorf("Dispatcher: robots.txt lookup yield an error %s", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Dispatcher: robots.txt returned an invalid http code: %d", resp.StatusCode)
	}
	rob, err := robotstxt.FromResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("Dispatcher: Parsing robots.txt yield an error %s", err)
	}
	return rob, nil
}
