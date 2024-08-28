package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	ldap "github.com/go-ldap/ldap/v3"
	"github.com/gomodule/redigo/redis"
	"gopkg.in/yaml.v3"
)

const (
	statusApproved = "approved"
	statusSynced   = "synced"
	statusPostfix  = ":status"
	passCheck      = 10
	delay          = 60
)

var config = flag.String("config", "", "path to config file")
var debug = flag.Bool("d", false, "enable debug mode")

type SyncConfig struct {
	debug   bool
	env     map[string]string
	trigger chan struct{}
}

func init() {
	flag.Parse()
}

func main() {

	c := new(SyncConfig)

	if *debug {
		c.debug = true
	}

	env := map[string]string{
		"LDAP_SOURCE_URI":           os.Getenv("LDAP_SOURCE_URI"),
		"LDAP_SOURCE_BASEDN":        os.Getenv("LDAP_SOURCE_BASEDN"),
		"LDAP_SOURCE_BINDDN":        os.Getenv("LDAP_SOURCE_BINDDN"),
		"LDAP_SOURCE_PASSWORD":      os.Getenv("LDAP_SOURCE_PASSWORD"),
		"LDAP_DESTINATION_URI":      os.Getenv("LDAP_DESTINATION_URI"),
		"LDAP_DESTINATION_BASEDN":   os.Getenv("LDAP_DESTINATION_BASEDN"),
		"LDAP_DESTINATION_BINDDN":   os.Getenv("LDAP_DESTINATION_BINDDN"),
		"LDAP_DESTINATION_PASSWORD": os.Getenv("LDAP_DESTINATION_PASSWORD"),
		"REDIS_URI":                 os.Getenv("REDIS_URI"),
		"REDIS_DATABASE":            os.Getenv("REDIS_DATABASE"),
		"REDIS_USERNAME":            os.Getenv("REDIS_USERNAME"),
		"REDIS_PASSWORD":            os.Getenv("REDIS_PASSWORD"),
	}

	c.env = env

	if *config != "" {
		// read yaml file
		yamlFile, err := os.ReadFile(*config)
		if err != nil {
			log.Fatal(err)
		}
		// parse yaml file
		var yamlConfig map[string]string
		if err := yaml.Unmarshal(yamlFile, &yamlConfig); err != nil {
			log.Fatal(err)
		}

		// Overriding env variables with yaml file
		for k, v := range yamlConfig {
			if _, ok := c.env[k]; ok {
				c.env[k] = v
			} else {
				log.Printf("Unknown key %s in yaml file", k)
			}
		}
	}

	// The trigger channel is used to signal the syncData method to start syncing data
	c.trigger = make(chan struct{})

	// Create the overall context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Start the sync manager
	go c.start(ctx)

	// Trigger the init sync
	c.trigger <- struct{}{}

	go func(ctx context.Context) {
		// Start the period manager
		if c.debug {
			fmt.Println("Starting period manager")
		}
	loop:
		for {
			select {
			case <-ctx.Done():
				if c.debug {
					fmt.Println("Period manager: Received done signal")
				}
				break loop

			case <-time.After(delay * time.Second):
				if c.debug {
					fmt.Println("Period manager: Triggering sync")
				}
				c.trigger <- struct{}{}
			}
		}
		if c.debug {
			fmt.Println("Stopping period manager")
		}
	}(ctx)

	//Instantiate a web server
	if c.debug {
		fmt.Println("Starting web server")
	}
	s := &http.Server{
		Addr:    ":8080",
		Handler: http.HandlerFunc(c.handle),
	}

	// Start the web server
	go func() {
		if err := s.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}()

	// Stop the server gracefully when ctrl-c, sigint or sigterm occurs
	<-ctx.Done()
	if c.debug {
		fmt.Printf("Stopping web server")
	}
	s.Shutdown(ctx)

}

func (c *SyncConfig) handle(w http.ResponseWriter, r *http.Request) {
	// handle incoming requests
	if c.debug {
		fmt.Println("Web: Received request")
	}

	// Trigger the sync
	if c.debug {
		fmt.Println("Web: Triggering sync")
	}
	c.trigger <- struct{}{}

	// Respond to the request
	w.WriteHeader(http.StatusOK)

	if c.debug {
		fmt.Println("Web: Responded to request")
	}
}

func (c *SyncConfig) syncData(pwCheck bool) {
	// sync data from source to destination
	if c.debug {
		fmt.Println("Starting sync")
	}

	uids2sync, err := c.getUids2Sync()
	if err != nil {
		fmt.Println("Warning: Unable to get uids to sync from redis. Err=", err)
	}

	if c.debug {
		fmt.Println("Uids to sync: ", uids2sync)
	}

	err = c.syncUsers(uids2sync, pwCheck)
	if err != nil {
		fmt.Println("Warning: Unable to sync users. Err=", err)
	}

	if c.debug {
		fmt.Println("Stopping sync")
	}
}

func (c *SyncConfig) start(ctx context.Context) {
	// start the sync manager

	if c.debug {
		fmt.Println("Starting sync manager")
	}

	trigger := c.trigger
	i := 0
loop:
	for {
		i++
		select {
		case <-ctx.Done():
			// The context is over, stop processing
			if c.debug {
				fmt.Println("Sync manager: Received done signal")
			}
			break loop
		case <-trigger:
			// Sync data
			if c.debug {
				fmt.Println("Sync manager: Received trigger")
			}
			c.syncData(i == passCheck)
			if i == passCheck {
				i = 0
			}
		}
	}

	if c.debug {
		fmt.Println("Stopping sync manager")
	}
}

func (c *SyncConfig) markSynced(uid string) error {
	if c.debug {
		fmt.Println("Marking user as synced. uid=" + uid)
	}

	r, err := c.getRedis()
	if err != nil {
		return err
	}
	defer r.Close()

	status, err := redis.String(r.Do("GET", uid+statusPostfix))
	if err != nil {
		return err
	}

	if status != statusApproved && status != statusSynced {
		return errors.New("Invalid status: " + status)
	}

	_, err = r.Do("SET", uid+statusPostfix, statusSynced)
	if err != nil {
		return err
	}
	return nil
}

func (c *SyncConfig) getUids2Sync() ([]string, error) {
	r, err := c.getRedis()
	if err != nil {
		return nil, err
	}
	defer r.Close()

	allUids, err := redis.Strings(r.Do("KEYS", "*"+statusPostfix))
	if err != nil {
		return nil, err
	}

	uids2sync := []string{}
	for _, uid := range allUids {
		status, err := redis.String(r.Do("GET", uid))
		if err != nil {
			return nil, err
		}
		if status == statusApproved || status == statusSynced {
			uid = strings.Split(uid, ":")[0]
			uids2sync = append(uids2sync, uid)
		}
	}

	return uids2sync, nil

}

func (c *SyncConfig) getRedis() (redis.Conn, error) {
	r, err := redis.Dial("tcp", c.env["REDIS_URI"])
	if err != nil {
		return nil, err
	}

	if c.env["REDIS_USERNAME"] != "" && c.env["REDIS_PASSWORD"] != "" {
		_, err = r.Do("AUTH", c.env["REDIS_USERNAME"], c.env["REDIS_PASSWORD"])
		if err != nil {
			return nil, err
		}
	}

	if c.env["REDIS_DATABASE"] != "" {
		_, err = r.Do("SELECT", c.env["REDIS_DATABASE"])
		if err != nil {
			return nil, err
		}
	}

	return r, nil
}

func (c *SyncConfig) syncUsers(uids []string, pwCheck bool) error {

	// Bind the source and destination LDAP servers - ls,ld
	ls, err := ldap.DialURL(c.env["LDAP_SOURCE_URI"])
	if err != nil {
		return err
	}
	defer ls.Close()

	err = ls.Bind(c.env["LDAP_SOURCE_BINDDN"], c.env["LDAP_SOURCE_PASSWORD"])
	if err != nil {
		return err
	}

	ld, err := ldap.DialURL(c.env["LDAP_DESTINATION_URI"])
	if err != nil {
		return err
	}
	defer ld.Close()

	err = ld.Bind(c.env["LDAP_DESTINATION_BINDDN"], c.env["LDAP_DESTINATION_PASSWORD"])
	if err != nil {
		return err
	}

	for _, uid := range uids {

		userExists := false

		// Check if user exists in the destination LDAP server
		searchRequest := ldap.NewSearchRequest(
			c.env["LDAP_DESTINATION_BASEDN"],
			ldap.ScopeWholeSubtree,
			ldap.NeverDerefAliases,
			0, 0, false,
			"(uid="+uid+")",
			[]string{"dn", "cn", "uid", "mail", "sn", "givenName", "userPassword"},
			nil,
		)

		destPassword := ""

		sr, err := ld.Search(searchRequest)
		if err != nil {
			return err
		}

		if len(sr.Entries) > 1 {
			fmt.Println("Warning: Multiple entries found for uid=" + uid)
			continue
		}

		if len(sr.Entries) == 1 {
			userExists = true
			if c.debug {
				fmt.Println("User exists in destination LDAP server. uid=" + uid)
			}
			if !pwCheck {
				// Mark user as synced
				err = c.markSynced(uid)
				if err != nil {
					return err
				}
				continue
			}
			destPassword = sr.Entries[0].GetAttributeValue("userPassword")
		}

		// Get user details from source LDAP server
		searchRequest = ldap.NewSearchRequest(
			c.env["LDAP_SOURCE_BASEDN"],
			ldap.ScopeWholeSubtree,
			ldap.NeverDerefAliases,
			0, 0, false,
			"(uid="+uid+")",
			[]string{"dn", "cn", "uid", "mail", "sn", "givenName", "userPassword"},
			nil,
		)

		newUser, err := ls.Search(searchRequest)
		if err != nil {
			return err
		}

		if len(newUser.Entries) != 1 {
			fmt.Println("WARNING: User does not exist or too many entries returned")
			continue
		}

		userDN := newUser.Entries[0].DN
		userSN := newUser.Entries[0].GetAttributeValue("sn")
		userEmail := newUser.Entries[0].GetAttributeValue("mail")
		userGivenName := newUser.Entries[0].GetAttributeValue("givenName")
		userPassword := newUser.Entries[0].GetAttributeValue("userPassword")

		if !userExists {
			// User does not exist in the destination LDAP server, create the user
			if c.debug {
				fmt.Println("User does not exist in destination LDAP server. uid=" + uid)
			}

			if c.debug {
				fmt.Println("User to create: ", userDN, userSN, userEmail, userGivenName)
			}

			addRequest := ldap.NewAddRequest(
				"uid="+uid+","+c.env["LDAP_DESTINATION_BASEDN"],
				[]ldap.Control{},
			)

			addRequest.Attribute("objectClass", []string{"inetOrgPerson"})
			addRequest.Attribute("cn", []string{userGivenName + " " + userSN})
			addRequest.Attribute("sn", []string{userSN})
			addRequest.Attribute("givenName", []string{userGivenName})
			addRequest.Attribute("mail", []string{userEmail})
			addRequest.Attribute("uid", []string{uid})
			addRequest.Attribute("userPassword", []string{userPassword})

			err = ld.Add(addRequest)
			if err != nil {
				fmt.Println(err)
				continue
			}

			// Mark user as synced
			err = c.markSynced(uid)
			if err != nil {
				fmt.Println(err)
				continue
			}

			if c.debug {
				fmt.Println("User created successfully. uid=" + uid)
			}
		} else {
			// The user exists in the destination LDAP server but the password needs to be checked
			if destPassword != userPassword {
				if c.debug {
					fmt.Println("User password needs to be updated. uid=" + uid)
				}

				modifyRequest := ldap.NewModifyRequest(userDN, []ldap.Control{})
				modifyRequest.Replace("userPassword", []string{userPassword})

				err = ld.Modify(modifyRequest)
				if err != nil {
					fmt.Println(err)
					continue
				}

				// Mark user as synced
				err = c.markSynced(uid)
				if err != nil {
					fmt.Println(err)
					continue
				}

				if c.debug {
					fmt.Println("User password updated successfully. uid=" + uid)
				}
			} else {
				if c.debug {
					fmt.Println("User password is up to date. uid=" + uid)
				}
				// Mark user as synced
				err = c.markSynced(uid)
				if err != nil {
					fmt.Println(err)
					continue
				}
			}
		}
	}
	return nil
}
