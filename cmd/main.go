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
	"slices"
	"strings"
	"time"

	ldap "github.com/go-ldap/ldap/v3"
	"github.com/gomodule/redigo/redis"
	"gopkg.in/yaml.v3"
)

// req:userid:status -> string
// req:userid:groups -> set

// req:userid:password -> string

const (
	statusApproved = "approved"
	statusSynced   = "synced"
	statusPostfix  = "status"
	prefix         = "req"
	infoPrefix     = "info"
	passCheck      = 10
	infoForce      = 10
	syncDelay      = 60
	infoDelay      = 60
)

var config = flag.String("config", "", "path to config file")
var debug = flag.Bool("d", false, "enable debug mode")

type SyncConfig struct {
	debug       bool
	env         map[string]string
	syncTrigger chan struct{}
	infoTrigger chan struct{}
}

type user struct {
	uid       string
	groups    []string
	password  string
	uidNumber string
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
		"LDAP_SOURCE_URI":                os.Getenv("LDAP_SOURCE_URI"),
		"LDAP_SOURCE_BASEDN":             os.Getenv("LDAP_SOURCE_BASEDN"),
		"LDAP_SOURCE_BINDDN":             os.Getenv("LDAP_SOURCE_BINDDN"),
		"LDAP_SOURCE_PASSWORD":           os.Getenv("LDAP_SOURCE_PASSWORD"),
		"LDAP_DESTINATION_URI":           os.Getenv("LDAP_DESTINATION_URI"),
		"LDAP_DESTINATION_USERS_BASEDN":  os.Getenv("LDAP_DESTINATION_USERS_BASEDN"),
		"LDAP_DESTINATION_GROUPS_BASEDN": os.Getenv("LDAP_DESTINATION_GROUPS_BASEDN"),
		"LDAP_DESTINATION_BINDDN":        os.Getenv("LDAP_DESTINATION_BINDDN"),
		"LDAP_DESTINATION_PASSWORD":      os.Getenv("LDAP_DESTINATION_PASSWORD"),
		"REDIS_URI":                      os.Getenv("REDIS_URI"),
		"REDIS_DATABASE":                 os.Getenv("REDIS_DATABASE"),
		"REDIS_USERNAME":                 os.Getenv("REDIS_USERNAME"),
		"REDIS_PASSWORD":                 os.Getenv("REDIS_PASSWORD"),
		"TRIGGER_PORT":                   os.Getenv("TRIGGER_PORT"),
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

	if c.debug {
		fmt.Println("Config: ", c.env)
	}

	// The trigger channel is used to signal the syncData method to start syncing data
	c.syncTrigger = make(chan struct{})

	// The info channel is used to signal the infoData method to start syncing users info
	c.infoTrigger = make(chan struct{})

	// Create the overall context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Start the sync manager
	go c.start(ctx)

	// Trigger the init sync
	c.syncTrigger <- struct{}{}

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

			case <-time.After(syncDelay * time.Second):
				if c.debug {
					fmt.Println("Period manager: Triggering sync")
				}
				c.syncTrigger <- struct{}{}
			}
		}
		if c.debug {
			fmt.Println("Stopping period manager")
		}
	}(ctx)

	go func(ctx context.Context) {
		// Start the info manager
		if c.debug {
			fmt.Println("Starting info manager")
		}
	loop:
		for {
			select {
			case <-ctx.Done():
				if c.debug {
					fmt.Println("Info manager: Received done signal")
				}
				break loop

			case <-time.After(infoDelay * time.Second):
				if c.debug {
					fmt.Println("Info manager: Triggering sync")
				}
				c.infoTrigger <- struct{}{}
			}
		}
		if c.debug {
			fmt.Println("Stopping info manager")
		}
	}(ctx)

	//Instantiate a web server
	if c.debug {
		fmt.Println("Starting web server")
	}
	s := &http.Server{
		Addr:    ":" + c.env["TRIGGER_PORT"],
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
	c.syncTrigger <- struct{}{}

	if c.debug {
		fmt.Println("Web: Triggering info")
	}
	c.infoTrigger <- struct{}{}

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

	alreadySyncedUids, err := c.getSyncedUids()
	if err != nil {
		fmt.Println("Warning: Unable to get synced uids from ldap. Err=", err)
	}

	fmt.Println("Already synced uids: ", alreadySyncedUids)

	uids2sync, err := c.getUids2Sync()
	if err != nil {
		fmt.Println("Warning: Unable to get uids to sync from redis. Err=", err)
	}

	if c.debug {
		fmt.Println("Uids to sync: ", uids2sync)
	}

	err = c.syncUsers(alreadySyncedUids, uids2sync, pwCheck)
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

	syncTrigger := c.syncTrigger
	infoTrigger := c.infoTrigger
	i := 0
	j := 0
loop:
	for {
		i++
		j++
		select {
		case <-ctx.Done():
			// The context is over, stop processing
			if c.debug {
				fmt.Println("Sync manager: Received done signal")
			}
			break loop
		case <-syncTrigger:
			// Sync data
			if c.debug {
				fmt.Println("Sync manager: Received trigger")
			}
			c.syncData(i == passCheck)
			if i == passCheck {
				i = 0
			}
		case <-infoTrigger:
			// Sync info
			if c.debug {
				fmt.Println("Sync manager: Received info trigger")
			}
			c.infoData(j == infoForce)
			if j == infoForce {
				j = 0
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

	status, err := redis.String(r.Do("GET", prefix+":"+uid+":"+statusPostfix))
	if err != nil {
		return err
	}

	if status != statusApproved && status != statusSynced {
		return errors.New("Invalid status: " + status)
	}

	_, err = r.Do("SET", prefix+":"+uid+":"+statusPostfix, statusSynced)
	if err != nil {
		return err
	}
	return nil
}

func (c *SyncConfig) getSyncedUids() ([]user, error) {
	ld, err := ldap.DialURL(c.env["LDAP_DESTINATION_URI"])
	if err != nil {
		return nil, err
	}
	defer ld.Close()

	err = ld.Bind(c.env["LDAP_DESTINATION_BINDDN"], c.env["LDAP_DESTINATION_PASSWORD"])
	if err != nil {
		return nil, err
	}

	syncedUids := make([]user, 0)

	searchRequest := ldap.NewSearchRequest(
		c.env["LDAP_DESTINATION_USERS_BASEDN"],
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0, 0, false,
		"(objectClass=posixAccount)",
		[]string{"uid"},
		nil,
	)

	sr, err := ld.Search(searchRequest)
	if err != nil {
		return nil, err
	}

	for _, entry := range sr.Entries {
		uid := entry.GetAttributeValue("uid")
		user := user{uid, nil, "", ""}
		syncedUids = append(syncedUids, user)
	}

	return syncedUids, nil
}

func (c *SyncConfig) getUids2Sync() ([]user, error) {
	r, err := c.getRedis()
	if err != nil {
		return nil, err
	}
	defer r.Close()

	allKeys, err := redis.Strings(r.Do("KEYS", prefix+":*:"+statusPostfix))
	if err != nil {
		return nil, err
	}

	uids2sync := make([]user, 0)
	for _, key := range allKeys {
		uid := strings.Split(key, ":")[1]
		status, err := redis.String(r.Do("GET", key))
		if err != nil {
			return nil, err
		}
		groups, err := redis.Strings(r.Do("SMEMBERS", prefix+":"+uid+":groups"))
		if err != nil {
			groups = make([]string, 0)
		}
		password, err := redis.String(r.Do("GET", prefix+":"+uid+":password"))
		if err != nil {
			password = ""
		}
		uidNumber, err := redis.String(r.Do("GET", prefix+":"+uid+":uidNumber"))
		if err != nil {
			uidNumber = ""
		}
		if status == statusApproved || status == statusSynced {
			user := user{uid, groups, password, uidNumber}
			uids2sync = append(uids2sync, user)
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

func (c *SyncConfig) groupsUpdate(ld *ldap.Conn, username string, groups []string) error {
	// Get all the possible groups from the destination LDAP server
	searchRequest := ldap.NewSearchRequest(
		c.env["LDAP_DESTINATION_GROUPS_BASEDN"],
		ldap.ScopeSingleLevel,
		ldap.NeverDerefAliases,
		0, 0, false,
		"(objectClass=posixGroup)",
		[]string{"cn"},
		nil,
	)

	sr, err := ld.Search(searchRequest)
	if err != nil {
		return err
	}

	if len(sr.Entries) == 0 {
		return errors.New("no groups found")
	}

	allGroups := make([]string, len(sr.Entries))

	for i, e := range sr.Entries {
		cn := e.GetAttributeValue("cn")
		allGroups[i] = cn
	}

	// Get the user's current groups
	searchRequest = ldap.NewSearchRequest(
		c.env["LDAP_DESTINATION_GROUPS_BASEDN"],
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0, 0, false,
		"(&(objectClass=posixGroup)(memberUid="+username+"))",
		[]string{"cn"},
		nil,
	)

	sr, err = ld.Search(searchRequest)
	if err != nil {
		return err
	}

	currentGroups := make([]string, len(sr.Entries))

	for i, e := range sr.Entries {
		cn := e.GetAttributeValue("cn")
		currentGroups[i] = cn
	}

	fmt.Println("Current groups: ", currentGroups)

	for _, g := range currentGroups {
		if !slices.Contains(groups, g) {
			// TODO Remove user from group
			modifyRequest := ldap.NewModifyRequest("cn="+g+","+c.env["LDAP_DESTINATION_GROUPS_BASEDN"], []ldap.Control{})
			modifyRequest.Delete("memberUid", []string{username})

			err = ld.Modify(modifyRequest)

			if err != nil {
				fmt.Println(err)
				continue
			}
		}
	}

	for _, g := range groups {
		if !slices.Contains(currentGroups, g) {
			// TODO Add user to group
			modifyRequest := ldap.NewModifyRequest("cn="+g+","+c.env["LDAP_DESTINATION_GROUPS_BASEDN"], []ldap.Control{})
			modifyRequest.Add("memberUid", []string{username})

			err = ld.Modify(modifyRequest)
			if err != nil {
				fmt.Println(err)
				continue
			}
		}
	}

	return nil
}

func (c *SyncConfig) infoData(force bool) error {
	r, err := c.getRedis()
	if err != nil {
		return err
	}
	defer r.Close()

	allKeys, err := redis.Strings(r.Do("KEYS", prefix+":*:"+statusPostfix))
	if err != nil {
		return err
	}

	uids := make(map[string]struct{})
	for _, key := range allKeys {
		uid := strings.Split(key, ":")[1]

		if force {
			uids[uid] = struct{}{}
		} else {
			_, err := redis.String(r.Do("GET", infoPrefix+":"+uid+":name"))
			if err != nil {
				uids[uid] = struct{}{}
			}
		}
	}

	ls, err := ldap.DialURL(c.env["LDAP_SOURCE_URI"])
	if err != nil {
		return err
	}
	defer ls.Close()

	err = ls.Bind(c.env["LDAP_SOURCE_BINDDN"], c.env["LDAP_SOURCE_PASSWORD"])
	if err != nil {
		return err
	}

	for uid := range uids {
		searchRequest := ldap.NewSearchRequest(
			c.env["LDAP_SOURCE_BASEDN"],
			ldap.ScopeWholeSubtree,
			ldap.NeverDerefAliases,
			0, 0, false,
			"(uid="+uid+")",
			[]string{"dn", "cn"},
			nil,
		)

		sr, err := ls.Search(searchRequest)
		if err != nil {
			continue
		}

		if len(sr.Entries) == 1 {
			name := sr.Entries[0].GetAttributeValue("cn")

			r.Do("SET", infoPrefix+":"+uid+":name", name)
		}
	}
	return nil
}

func (c *SyncConfig) syncUsers(already []user, users []user, pwCheck bool) error {

	alreadyMap := make(map[string]struct{})
	for _, u := range already {
		alreadyMap[u.uid] = struct{}{}
	}

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

	for _, u := range users {

		uid := u.uid
		localPassword := u.password
		groups := u.groups
		localUidNumber := u.uidNumber

		userExists := false

		// Check if user exists in the destination LDAP server
		searchRequest := ldap.NewSearchRequest(
			c.env["LDAP_DESTINATION_USERS_BASEDN"],
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
			delete(alreadyMap, uid)
			if c.debug {
				fmt.Println("User exists in destination LDAP server. uid=" + uid)
			}
			if !pwCheck {
				// Mark user as synced
				err = c.markSynced(uid)
				if err != nil {
					return err
				}
				err = c.groupsUpdate(ld, uid, groups)
				if err != nil {
					return err
				}
				continue
			}
			if localPassword == "" {
				destPassword = sr.Entries[0].GetAttributeValue("userPassword")
			} else {
				destPassword = localPassword
			}
		}

		// Get user details from source LDAP server
		searchRequest = ldap.NewSearchRequest(
			c.env["LDAP_SOURCE_BASEDN"],
			ldap.ScopeWholeSubtree,
			ldap.NeverDerefAliases,
			0, 0, false,
			"(uid="+uid+")",
			[]string{"dn", "cn", "uid", "mail", "sn", "givenName", "userPassword", "uidNumber", "homeDirectory"},
			nil,
		)

		newUser, err := ls.Search(searchRequest)
		if err != nil {
			return err
		}

		var userDN, userSN, userEmail, userGivenName, userPassword, uidNumber, homeDirectory string

		if len(newUser.Entries) == 0 {
			if localPassword == "" || localUidNumber == "" {
				fmt.Println("Warning: User not found in source LDAP server. uid=" + uid)
				continue
			} else {
				userSN = uid
				userEmail = uid + "@uninuvolafakeemail.it"
				userGivenName = "Local User"
				userPassword = localPassword
				uidNumber = localUidNumber
				homeDirectory = "/home/" + uid
				userDN = userGivenName + " " + userSN
			}
		} else if len(newUser.Entries) > 1 {
			fmt.Println("Warning: Multiple entries found for uid=" + uid)
			continue
		} else {
			userDN = newUser.Entries[0].DN
			userSN = newUser.Entries[0].GetAttributeValue("sn")
			userEmail = newUser.Entries[0].GetAttributeValue("mail")
			userGivenName = newUser.Entries[0].GetAttributeValue("givenName")
			userPassword = newUser.Entries[0].GetAttributeValue("userPassword")
			uidNumber = newUser.Entries[0].GetAttributeValue("uidNumber")
			homeDirectory = newUser.Entries[0].GetAttributeValue("homeDirectory")
		}

		if !userExists {
			// User does not exist in the destination LDAP server, create the user
			if c.debug {
				fmt.Println("User does not exist in destination LDAP server. uid=" + uid)
			}

			if c.debug {
				fmt.Println("User to create: ", userDN, userSN, userEmail, userGivenName)
			}

			addRequest := ldap.NewAddRequest(
				"cn="+userGivenName+" "+userSN+","+c.env["LDAP_DESTINATION_USERS_BASEDN"],
				[]ldap.Control{},
			)

			addRequest.Attribute("objectClass", []string{"inetOrgPerson", "posixAccount"})
			addRequest.Attribute("cn", []string{userGivenName + " " + userSN})
			addRequest.Attribute("sn", []string{userSN})
			addRequest.Attribute("givenName", []string{userGivenName})
			addRequest.Attribute("mail", []string{userEmail})
			addRequest.Attribute("uid", []string{uid})
			addRequest.Attribute("uidNumber", []string{uidNumber})
			addRequest.Attribute("homeDirectory", []string{homeDirectory})
			addRequest.Attribute("gidNumber", []string{"500"})
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
			err = c.groupsUpdate(ld, uid, groups)
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
				err = c.groupsUpdate(ld, uid, groups)
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
				err = c.groupsUpdate(ld, uid, groups)
				if err != nil {
					fmt.Println(err)
					continue
				}
			}
		}
	}

	fmt.Println("Users to delete: ", alreadyMap)

	for uid := range alreadyMap {
		// Delete users that are no longer in the source LDAP server
		searchRequest := ldap.NewSearchRequest(
			c.env["LDAP_DESTINATION_USERS_BASEDN"],
			ldap.ScopeWholeSubtree,
			ldap.NeverDerefAliases,
			0, 0, false,
			"(uid="+uid+")",
			[]string{"dn"},
			nil,
		)

		sr, err := ld.Search(searchRequest)
		if err != nil {
			return err
		}

		if len(sr.Entries) == 0 {
			fmt.Println("Warning: User not found in destination LDAP server. uid=" + uid)
			continue
		} else if len(sr.Entries) > 1 {
			fmt.Println("Warning: Multiple entries found for uid=" + uid)
			continue
		}

		deleteRequest := ldap.NewDelRequest(sr.Entries[0].DN, []ldap.Control{})
		err = ld.Del(deleteRequest)
		if err != nil {
			fmt.Println(err)
			continue
		}

		err = c.groupsUpdate(ld, uid, []string{})
		if err != nil {
			fmt.Println(err)
			continue
		}
	}

	return nil
}
