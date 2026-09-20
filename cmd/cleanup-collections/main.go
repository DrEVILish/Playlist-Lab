// One-off cleanup: delete every Plex-native (Kometa-built, not tracked by
// Playlist Lab) collection from server 15, skipping any collection this app
// itself manages. Throwaway - delete this directory after running once.
package main

import (
	"fmt"
	"os"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

func main() {
	const serverID = int64(15)

	sqlDB, err := db.Open("data/playlist-lab.db")
	if err != nil {
		fmt.Println("open db:", err)
		os.Exit(1)
	}
	defer sqlDB.Close()

	server, err := db.GetUserServerByID(sqlDB, serverID)
	if err != nil || server == nil {
		fmt.Println("server not found:", err)
		os.Exit(1)
	}
	user, err := db.GetUserByID(sqlDB, server.UserID)
	if err != nil || user == nil {
		fmt.Println("user not found:", err)
		os.Exit(1)
	}

	// Managed (Playlist Lab-tracked) collections on this server must be kept -
	// their plex_collection_id is excluded from deletion.
	rows, err := db.GetUserCollections(sqlDB, user.ID)
	if err != nil {
		fmt.Println("load managed collections:", err)
		os.Exit(1)
	}
	keep := map[string]bool{}
	for _, c := range rows {
		if c.ServerID == serverID && c.PlexCollectionID.Valid {
			keep[c.PlexCollectionID.String] = true
		}
	}
	fmt.Println("keeping", len(keep), "Playlist Lab-managed collection(s) on server", serverID)

	clientID := os.Getenv("PLEX_CLIENT_ID")
	if clientID == "" {
		clientID = "playlist-lab-server"
	}
	token := plex.ResolveToken(user.PlexToken, server.AccessToken.String)
	client := plex.NewClient(server.ServerURL, token, clientID, "Playlist Lab")

	libs, err := client.GetLibraries()
	if err != nil {
		fmt.Println("get libraries:", err)
		os.Exit(1)
	}

	deleted, kept, failed := 0, 0, 0
	for _, lib := range libs {
		colls, err := client.GetCollections(lib.ID)
		if err != nil {
			fmt.Println("get collections for library", lib.Name, ":", err)
			continue
		}
		for _, c := range colls {
			if keep[c.RatingKey] {
				kept++
				continue
			}
			if err := client.DeleteCollection(c.RatingKey); err != nil {
				fmt.Println("failed to delete:", c.Title, err)
				failed++
				continue
			}
			deleted++
		}
	}
	fmt.Printf("done: deleted=%d kept=%d failed=%d\n", deleted, kept, failed)
}
