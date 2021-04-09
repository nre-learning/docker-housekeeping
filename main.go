package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	cli "github.com/urfave/cli"
)

const (
	dockerUsernameEnv = "DOCKERHUB_USERNAME"
	dockerPasswordEnv = "DOCKERHUB_PASSWORD"
)

func main() {

	app := &cli.App{
		Name:    "docker-housekeeping",
		Version: "0.1.0",
		Usage:   "A tool for various docker housekeeping tasks for the NRE Labs platform",

		Before: func(c *cli.Context) error {
			return nil
		},

		Commands: []cli.Command{
			{
				Name:    "retag",
				Aliases: []string{},
				Usage:   "Copy an existing tag to a new tag (useful for re-tagging images for preview purposes)",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "repository",
						Required: true,
					},
					&cli.StringFlag{
						Name:     "oldTag",
						Required: true,
					},
					&cli.StringFlag{
						Name:     "newTag",
						Required: true,
					},
				},
				Action: func(c *cli.Context) error {

					username, found := os.LookupEnv(dockerUsernameEnv)
					if !found {
						log.Error(dockerUsernameEnv + " not found in environment")
						return errors.New(dockerUsernameEnv + " not found in environment")
					}

					password, found := os.LookupEnv(dockerPasswordEnv)
					if !found {
						log.Error(dockerPasswordEnv + " not found in environment")
						return errors.New(dockerPasswordEnv + " not found in environment")
					}

					var (
						repository = c.String("repository")
						oldTag     = c.String("oldTag")
						newTag     = c.String("newTag")
					)

					token, err := loginRegistry(repository, username, password)
					if err != nil {
						return errors.New("failed to authenticate: " + err.Error())
					}

					manifest, err := pullManifest(token, repository, oldTag)
					if err != nil {
						return errors.New("failed to pull manifest: " + err.Error())
					}

					if err := pushManifest(token, repository, newTag, manifest); err != nil {
						return errors.New("failed to push manifest: " + err.Error())
					}

					separator := ":"
					if strings.HasPrefix(oldTag, "sha256:") {
						separator = "@"
					}

					fmt.Printf("Retagged %s%s%s as %s:%s\n", repository, separator, oldTag, repository, newTag)

					return nil
				},
			},
			{
				Name:    "prune-preview-tags",
				Aliases: []string{},
				Usage:   "Prune preview tags from docker hub",
				Action: func(c *cli.Context) error {

					username, found := os.LookupEnv(dockerUsernameEnv)
					if !found {
						log.Error(dockerUsernameEnv + " not found in environment")
						return errors.New(dockerUsernameEnv + " not found in environment")
					}

					password, found := os.LookupEnv(dockerPasswordEnv)
					if !found {
						log.Error(dockerPasswordEnv + " not found in environment")
						return errors.New(dockerPasswordEnv + " not found in environment")
					}

					images, err := getAllImages()
					if err != nil {
						log.Error(err)
					}

					hubToken, err := loginHub(username, password)
					if err != nil {
						log.Error("failed to authenticate: " + err.Error())
						return errors.New("failed to authenticate: " + err.Error())
					}

					for i := range images {
						repository := fmt.Sprintf("antidotelabs/%s", images[i])

						registryToken, err := loginRegistry(repository, username, password)
						if err != nil {
							log.Error("failed to authenticate: " + err.Error())
							return errors.New("failed to authenticate: " + err.Error())
						}

						tags, err := listPreviewTags(registryToken, repository)
						if err != nil {
							log.Error(err.Error())
							continue
							// This happens because there are a bunch of old images, specifically platform images, in the same org, and this can happen when
							// there simply aren't any tags. Shouldn't happen with curriculum images. Once curriculum images are split into their own org, we can change this
							// to return an error upstream. For now, continuing to the next image is appropriate.
						}

						for j := range tags {
							t, err := getTagLastUpdate(repository, tags[j])
							if err != nil {
								log.Error(err.Error())
								return errors.New("failed to get last tag update: " + err.Error())
							}

							log.Infof("TAG %s LAST UPDATED %s (%f hours ago)", tags[j], t, time.Since(t).Hours())
							if time.Since(t).Hours() > 24 {
								log.Warnf("Deleting tag %s", tags[j])
								err = deleteTag(hubToken, repository, tags[j])
								if err != nil {
									log.Errorf(err.Error())
									return fmt.Errorf("failed to delete tag %s - %v", tags[j], err)
								}
							}
						}
					}

					return nil
				},
			},
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

func loginRegistry(repo string, username string, password string) (string, error) {
	var (
		client = http.DefaultClient
		url    = "https://auth.docker.io/token?service=registry.docker.io&scope=repository:" + repo + ":pull,push"
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	req.SetBasicAuth(username, password)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", errors.New(resp.Status)
	}

	bodyText, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var data struct {
		Details string `json:"details"`
		Token   string `json:"token"`
	}

	if err := json.Unmarshal(bodyText, &data); err != nil {
		return "", err
	}

	if data.Token == "" {
		return "", errors.New("empty token")
	}

	return data.Token, nil
}

func loginHub(username string, password string) (string, error) {

	var (
		client = http.DefaultClient
		url    = "https://hub.docker.com/v2/users/login"
	)

	var jsonData = []byte(fmt.Sprintf(`{
		"username": "%s",
		"password": "%s"
	}`, username, password))

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", errors.New(resp.Status)
	}

	bodyText, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var data struct {
		Details string `json:"details"`
		Token   string `json:"token"`
	}

	if err := json.Unmarshal(bodyText, &data); err != nil {
		return "", err
	}

	if data.Token == "" {
		return "", errors.New("empty token")
	}

	return data.Token, nil
}

func pullManifest(token string, repository string, tag string) ([]byte, error) {
	var (
		client = http.DefaultClient

		// This is the registry API, which is different from the docker hub API also used by this app. Retagging will require
		// the registry API.
		url = "https://index.docker.io/v2/" + repository + "/manifests/" + tag
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}

	bodyText, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return bodyText, nil
}

func pushManifest(token string, repository string, tag string, manifest []byte) error {
	var (
		client = http.DefaultClient
		url    = "https://index.docker.io/v2/" + repository + "/manifests/" + tag
	)

	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(manifest))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-type", "application/vnd.docker.distribution.manifest.v2+json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusCreated {
		return errors.New(resp.Status)
	}

	return nil
}

func listPreviewTags(token, repository string) ([]string, error) {

	// TODO - convert this to use the hub API and see if this gets you the timestamp info in the same call so you can eliminate a GET
	// later on
	var (
		client = http.DefaultClient
		url    = "https://index.docker.io/v2/" + repository + "/tags/list"
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}

	bodyText, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var data struct {
		Name string   `json:"Name"`
		Tags []string `json:"tags"`
	}

	if err := json.Unmarshal(bodyText, &data); err != nil {
		return []string{}, err
	}

	var tags []string
	for i := range data.Tags {
		if strings.HasPrefix(data.Tags[i], "preview-") {
			tags = append(tags, data.Tags[i])
		}
	}

	log.Infof("Found preview tags for repository %s: %v", repository, tags)

	return tags, nil
}

// Doesn't need to be authenticated - even private images can be publicly listed
func getAllImages() ([]string, error) {
	var (
		client = http.DefaultClient

		// TODO - curriculum and platform images are mixed here. Might want to think about separating these. However, filtering on preview-abcdef tag
		// should only apply to curriculum images so this is okay for now.
		url = "https://hub.docker.com/v2/repositories/antidotelabs/?page_size=100"
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}

	bodyText, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var data struct {
		Count   int `json:"count"`
		Results []struct {
			User string `json:"user"`
			Name string `json:"name"`
		} `json:"results"`
	}

	if err := json.Unmarshal(bodyText, &data); err != nil {
		return []string{}, err
	}

	var images []string
	for i := range data.Results {
		images = append(images, data.Results[i].Name)
	}

	return images, nil
}

func getTagLastUpdate(repository, tag string) (time.Time, error) {
	var (
		client = http.DefaultClient
		url    = fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/tags/%s", repository, tag)
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return time.Time{}, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return time.Time{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return time.Time{}, errors.New(resp.Status)
	}

	bodyText, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return time.Time{}, err
	}

	// {
	// 	"creator":9777905,
	// 	"id":142622778,
	// 	"image_id":null,
	// 	"images":[
	// 		{
	// 			"architecture":"amd64",
	// 			"features":null,
	// 			"variant":null,
	// 			"digest":"sha256:cdf54fd8eb50dc49dfc4b27b749fa115907bfa6794d52c4bf6aaf87183c7474b",
	// 			"os":"linux",
	// 			"os_features":null,
	// 			"os_version":null,
	// 			"size":445842141,
	// 			"status":"active",
	// 			"last_pulled":"2021-04-09T14:56:27.672035Z",
	// 			"last_pushed":"2021-03-29T15:35:28.10331Z"
	// 		}
	// 	],
	// 	"last_updated":"2021-03-23T14:28:48.584886Z",
	// 	"last_updater":9777905,
	// 	"last_updater_username":"nrelabs",
	// 	"name":"preview-a0jph6u",
	// 	"repository":6276803,
	// 	"full_size":445842141,
	// 	"v2":true,
	// 	"tag_status":"active",
	// 	"tag_last_pulled":"2021-04-09T14:56:27.672035Z",
	// 	"tag_last_pushed":"2021-03-23T14:28:48.584886Z"
	// }

	var data struct {
		LastUpdated   string `json:"last_updated"`
		TagLastPushed string `json:"tag_last_pushed"`
	}

	if err := json.Unmarshal(bodyText, &data); err != nil {
		return time.Time{}, err
	}

	t, err := time.Parse(time.RFC3339, data.LastUpdated)
	if err != nil {
		return time.Time{}, err
	}

	return t, nil
}

func deleteTag(token, repository, tag string) error {
	var (
		client = http.DefaultClient
		url    = fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/tags/%s/", repository, tag)
	)

	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", fmt.Sprintf("JWT %s", token))
	req.Header.Set("Accept", "application/json")

	log.Warnf("SENDING DELETE TO %s", url)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusNoContent {
		return errors.New(resp.Status)
	}

	_, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return nil
}
