package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/go-pg/pg/v9"
	"github.com/sirupsen/logrus"
	"github.com/slack-go/slack"
	"github.com/ttgmpsn/mira"
	miramodels "github.com/ttgmpsn/mira/models"
	prefixed "github.com/x-cray/logrus-prefixed-formatter"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

type botConfig struct {
	LogFile   string
	Subreddit string

	Slack struct {
		BotUserToken        string
		NotificationChannel string
	}

	Database struct {
		Username string
		Password string
		Host     string
		Database string
	}

	Reddit mira.Credentials

	Services struct {
		YTAPIKey string
	}
}

var config botConfig
var db *pg.DB

var slackAPI *slack.Client
var slackInfo *slack.AuthTestResponse

var reddit *mira.Reddit

func init() {
	logrus.SetFormatter(&prefixed.TextFormatter{})
	logrus.SetLevel(logrus.DebugLevel)
	logrus.SetOutput(os.Stdout)
}

var log = logrus.WithField("prefix", "main")

func main() {
	var logfile, conffile string
	flag.StringVar(&logfile, "log", "", "path to a logfile (optional, stdout & debug enabled when skipped)")
	flag.StringVar(&conffile, "conf", "", "path to a configfile (required)")
	flag.Parse()

	if logfile != "" {
		f, err := os.OpenFile(logfile, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0660)
		if err != nil {
			panic("error opening log file")
		}
		defer f.Close()
		logrus.SetOutput(f)
		logrus.SetLevel(logrus.InfoLevel)
	}

	var err error

	log.Info("Starting SnooYTBust")
	log.Info("Reading config")
	if len(conffile) == 0 {
		log.Fatal("Please set the conffile flag")
	}
	if _, err = toml.DecodeFile(conffile, &config); err != nil {
		log.WithError(err).WithField("file", conffile).Fatal("reading config")
	}

	// Connect to DB
	db = pg.Connect(&pg.Options{
		User:     config.Database.Username,
		Password: config.Database.Password,
		Database: config.Database.Database,
		Addr:     config.Database.Host,
	})

	// listen to Slack
	if len(config.Slack.NotificationChannel) > 0 {
		slackAPI = slack.New(
			config.Slack.BotUserToken,
			slack.OptionDebug(false),
		)

		slackInfo, err = slackAPI.AuthTest()
		if err != nil {
			log.WithError(err).Fatal("Slack Auth failed")
		}
		log.Infof("Connected to Slack as %s (ID %s)", slackInfo.User, slackInfo.UserID)
	}

	// Login to Reddit
	reddit = mira.Init(config.Reddit)
	err = reddit.LoginAuth()
	if err != nil {
		log.WithError(err).Fatal("Reddit Auth failed")
	}
	rMeObj, err := reddit.Me().Info()
	if err != nil {
		log.WithError(err).Fatal("Reddit Auth failed")
	}
	rMe, _ := rMeObj.(*miramodels.Me)
	log.WithField("prefix", "reddit").Infof("Connected to Reddit as /u/%s", rMe.Name)

	// Login to YouTube
	ctx := context.Background()
	ytService, err := youtube.NewService(ctx, option.WithAPIKey(config.Services.YTAPIKey))
	if err != nil {
		log.WithError(err).Fatal("YouTube Auth failed")
	}

	pS, err := reddit.Subreddit(config.Subreddit).StreamPosts()
	if err != nil {
		log.WithError(err).Fatal("Could not create post stream")
	}
	cS, err := reddit.Subreddit(config.Subreddit).StreamComments()
	if err != nil {
		log.WithError(err).Fatal("Could not create comment stream")
	}
	for {
		var s miramodels.Submission
		var pSC, cSC bool
		select {
		case s = <-pS.C:
			if s == nil {
				pSC = true
			}
		case s = <-cS.C:
			if s == nil {
				cSC = true
			}
		}
		if s == nil {
			log.Warn("looks like one channel is closed?")
			if pSC {
				log.Info("post channel was closed, recreating")
				chanCreated := false
				for !chanCreated {
					pS, err = reddit.Subreddit(config.Subreddit).StreamPosts()
					if err != nil {
						log.WithError(err).Warn("Could not create post stream, trying again in a minute (reddit down?)...")
						time.Sleep(1 * time.Minute)
						continue
					}
					chanCreated = true
				}
			}
			if cSC {
				log.Info("comment channel was closed, recreating")
				chanCreated := false
				for !chanCreated {
					cS, err = reddit.Subreddit(config.Subreddit).StreamComments()
					if err != nil {
						log.WithError(err).Warn("Could not create comment stream, trying again in a minute (reddit down?)...")
						time.Sleep(1 * time.Minute)
						continue
					}
					chanCreated = true
				}
			}
			continue
		}
		links := extractYT(s.GetBody())
		if len(links) == 0 {
			log.WithField("thing_id", s.GetID()).Debug("no video")
		}
		for _, link := range links {
			l := log.WithField("video_id", link).WithField("thing_id", s.GetID())
			l.Debug("found YT link")
			call := ytService.Videos.List("id,snippet").Id(link).Fields("items(id,snippet(channelId,channelTitle))")
			resp, err := call.Do()
			if err != nil {
				log.WithError(err).Error("YT API Error")
				continue
			}

			if len(resp.Items) < 1 {
				// No item found
				continue
			}

			for _, i := range resp.Items {
				l.Infof("YT video found - Channel: %s (ID %s)", i.Snippet.ChannelTitle, i.Snippet.ChannelId)

				var res struct {
					ID uint64
				}

				r, err := db.QueryOne(&res, "SELECT id FROM dtg_blacklist WHERE media_channel_id = ? AND media_platform_id = 1 LIMIT 1", i.Snippet.ChannelId)
				if err != nil && err != pg.ErrNoRows {
					l.WithError(err).Error("DB query failed")
					continue
				}

				if err == pg.ErrNoRows || r.RowsReturned() == 0 {
					continue
				}
				l.Infof("Blacklisted (ID %d), removing", res.ID)

				switch s.GetID().Type() {
				case miramodels.KPost:
					reddit.Post(string(s.GetID())).Remove(false)
				case miramodels.KComment:
					reddit.Comment(string(s.GetID())).Remove(false)
				}

				if len(config.Slack.NotificationChannel) > 0 {
					slackAPI.PostMessage(config.Slack.NotificationChannel, slack.MsgOptionAsUser(true), slack.MsgOptionUser(slackInfo.UserID), slack.MsgOptionDisableLinkUnfurl(),
						slack.MsgOptionText(
							fmt.Sprintf("*Attention:* _The following item has been removed._\n*Channel Author:* %s\n*Reddit Author:* %s\n*Subreddit:* /r/%s\n*Reddit Link:* %s",
								resp.Items[0].Snippet.ChannelTitle, s.GetAuthor(), s.GetSubreddit(), s.GetURL(),
							),
							false,
						),
					)
				}
			}
		}
	}
}
