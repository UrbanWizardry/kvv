package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"slices"
	"sort"
	"time"

	"github.com/atotto/clipboard"
	"gopkg.in/yaml.v3"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/keyvault/azsecrets"
	"github.com/gdamore/tcell/v2"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
)

var (
	secrets       []*azsecrets.SecretItem
	app           *tview.Application
	top           *tview.Grid
	kvDropdown    *tview.DropDown
	info          *tview.Table
	client        *azsecrets.Client
	cred          *azidentity.DefaultAzureCredential
	secretValue   *tview.TextView
	keys          *tview.Table
	versionsTable *tview.Table
)

const (
	VANITY_LOGO = `   __ ___   ___   __
  / //_/ | / / | / /
 / ,<  | |/ /| |/ / 
/_/|_| |___/ |___/ 
Key Vault Viewer
Version 0.1.0
`
)

type kvvConfig struct {
	Vaults []string `yaml:"vaults"`
}

func main() {
	vaults := []string{}
	if len(os.Args) > 1 {
		vaults = append(vaults, os.Args[1])
	}

	// Attempt to get config file
	home, err := os.UserHomeDir()
	if err == nil {
		kvvConfigFile := path.Join(home, ".kvv")
		_, err = os.Stat(kvvConfigFile)
		if err == nil {
			// Found a config file.
			data, err := os.ReadFile(kvvConfigFile)
			if err != nil {
				panic(fmt.Errorf("error reading confg file at %s", kvvConfigFile))
			}

			var config kvvConfig
			err = yaml.Unmarshal(data, &config)
			if err != nil {
				panic(errors.Wrapf(err, "error unmarshalling confg file at %s", kvvConfigFile))
			}

			for _, vault := range config.Vaults {
				if !slices.Contains(vaults, vault) {
					vaults = append(vaults, vault)
				}
			}
		}
	}

	if len(vaults) == 0 {
		fmt.Println("No vaults to open, exiting")
		os.Exit(1)
	}

	cred, err = azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		log.Fatalf("failed to obtain a credential: %v", err)
	}

	// Top stuff
	kvDropdown = tview.NewDropDown().
		SetFocusedStyle(
			tcell.Style{}.
				Background(tcell.ColorBlue).
				Foreground(tcell.ColorBlack),
		).
		SetFieldStyle(
			tcell.Style{}.
				Background(tcell.ColorBlack).
				Foreground(tcell.ColorAntiqueWhite),
		).
		SetLabel("Vault: ")

	kvDropdown.
		SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyEscape {
				setTableFocus(keys)
				return nil
			}
			return event
		})
	kvDropdown.SetBorder(true)

	for _, vaultUri := range vaults {
		kvDropdown.AddOption(vaultUri, func() {
			connect(vaultUri)
			fetchSecrets()
			updateKeys()
			setTableFocus(keys)
		})
	}

	info = tview.NewTable().
		SetSelectable(false, false)

	info.SetCell(1, 0, tview.NewTableCell("<v> Change vault").SetStyle(tcell.Style{}.Bold(true)))
	info.SetCell(2, 0, tview.NewTableCell("<c> Copy selected secret").SetStyle(tcell.Style{}.Bold(true)))
	info.SetCell(3, 0, tview.NewTableCell("<q> Quit KVV").SetStyle(tcell.Style{}.Bold(true)))

	logo := tview.NewTextArea().SetText(VANITY_LOGO, false)

	top = tview.NewGrid().
		SetRows(3, 0).
		SetColumns(0, 23).
		AddItem(kvDropdown, 0, 0, 1, 1, 0, 0, false).
		AddItem(info, 1, 0, 1, 1, 0, 0, false).
		AddItem(logo, 0, 1, 2, 1, 0, 0, false)

	// Navigable list of secrets
	keys = tview.NewTable().
		SetBorders(false).
		SetSelectedFunc(secretSelected).
		SetSelectable(true, false).
		Select(0, 0)

	keys.SetBorderPadding(1, 1, 1, 1)

	// Navigable list of secret versions
	versionsTable = tview.NewTable().
		SetBorders(false).
		SetSelectedFunc(versionSelected).
		SetSelectable(true, false).
		Select(0, 0)

	// versionsTable.
	// 	SetBorderPadding(1, 1, 1, 1)

	versionsTable.
		SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyEscape {
				clearVersionsAndValue()
				setTableFocus(keys)
				return nil
			}
			return event
		})

	secretValue = tview.NewTextView()
	secretValue.
		SetBorderPadding(1, 1, 1, 1).
		SetBorder(false)

	// Page layout
	pageGrid := tview.NewGrid().
		SetRows(7, 3, 0).
		SetColumns(-2, -4).
		SetBorders(true).
		AddItem(top, 0, 0, 1, 2, 0, 0, false).
		AddItem(keys, 1, 0, 2, 1, 0, 0, true).
		AddItem(secretValue, 1, 1, 1, 1, 0, 0, false).
		AddItem(versionsTable, 2, 1, 1, 1, 0, 0, false)

	pageGrid.
		SetBorderStyle(tcell.Style{}.Bold(true))

	app = tview.NewApplication().SetRoot(pageGrid, true)
	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		// Block Ctrl-C to exit
		if event.Key() == tcell.KeyCtrlC {
			return nil
		}

		switch event.Rune() {
		case 'c':
			copySecret()
			return nil
		case 'v':
			unfocusTables()
			app.SetFocus(kvDropdown)
			return nil
		case 'q':
			app.Stop()
			return nil
		}
		return event
	})

	// Ready to go, get secrets from the initial vault selection
	kvDropdown.SetCurrentOption(0)
	fetchSecrets()

	if err := app.Run(); err != nil {
		panic(err)
	}

}

// func vaultOptionChanged() {
// 	_, vaultUri := kvDropdown.GetCurrentOption()
// 	connect(vaultUri)
// 	fetchSecrets()
// 	setTableFocus(keys)
// }

func connect(vaultUri string) {
	var err error
	// Establish a connection to the Key Vault client
	client, err = azsecrets.NewClient(vaultUri, cred, nil)
	if err != nil {
		panic(err)
	}
}

func fetchSecrets() {
	secretPager := client.NewListSecretsPager(nil)
	secrets = []*azsecrets.SecretItem{}

	for secretPager.More() {
		resp, err := secretPager.NextPage(context.Background())
		if err != nil {
			panic(errors.Wrap(err, "failed to get paged secrets"))
		}

		secrets = append(secrets, resp.Value...)
	}
}

func updateKeys() {
	clearVersionsAndValue()
	keys.Clear()
	for row, secret := range secrets {
		keys.SetCell(row, 0, tview.NewTableCell(secret.ID.Name()))
	}
}

func clearVersionsAndValue() {
	versionsTable.Clear()
	secretValue.Clear()
}

func unfocusTables() {
	focusables := []*tview.Table{keys, versionsTable}

	for _, focusable := range focusables {
		setTableTextStyleUnfocused(focusable)
	}
}

func setTableFocus(table *tview.Table) {
	unfocusTables()
	setTableTextStyleFocused(table)
	app.SetFocus(table)
}

func setTableTextStyleFocused(table *tview.Table) {
	style := tcell.Style{}.Foreground(tcell.ColorBlue).Bold(true).Background(tcell.ColorBlack)
	setTableTextStyle(table, style)
}

func setTableTextStyleUnfocused(table *tview.Table) {
	style := tcell.Style{}.Foreground(tcell.ColorAntiqueWhite).Bold(false).Background(tcell.ColorBlack)
	setTableTextStyle(table, style)
}

func setTableTextStyle(table *tview.Table, style tcell.Style) {
	for row := range table.GetRowCount() {
		for col := range table.GetColumnCount() {
			table.GetCell(row, col).SetStyle(style)
		}
	}
}

func secretSelected(row int, col int) {
	secretCell := keys.GetCell(row, col)
	if secretCell == nil {
		return
	}

	secretName := secretCell.Text

	versions, err := getSecretVersions(secretName, client)
	if err != nil {
		return
	}

	showVersions(versions)

	// versionId, err := getLatestSecretVersionIdFromVersions(versions)
	// if err != nil {
	// 	// don't panic, just chill
	// 	return
	// }

	// resp, err := client.GetSecret(context.Background(), secretName, versionId, nil)
	// if err != nil {
	// 	// chill
	// 	fmt.Println("error getting secret")
	// 	return
	// }

	// secret := resp.SecretBundle.Value
	// if secret != nil {
	// 	showValue(*secret)
	// }

}

func showValue(value string) {
	secretValue.Clear()
	secretValue.SetText(value)
}

func showVersions(versions []secretVersion) {
	versionsTable.Clear()
	for r, version := range sortSecretVersionsNewestFirst(versions) {
		versionsTable.SetCell(r, 0, tview.NewTableCell(version.Id).SetExpansion(2))
		versionsTable.SetCell(r, 1, tview.NewTableCell(version.Created.String()))
	}

	versionsTable.Select(0, 0)
	versionSelected(0, 0)
	setTableFocus(versionsTable)
}

func versionSelected(row int, _ int) {

	keyIndex, _ := keys.GetSelection()
	secretName := keys.GetCell(keyIndex, 0).Text
	versionId := versionsTable.GetCell(row, 0).Text //getLatestSecretVersionId(secretName, client)
	resp, err := client.GetSecret(context.Background(), secretName, versionId, nil)
	if err != nil {
		// chill
		showValue(fmt.Sprint("error getting secret: ", err))
		return
	}

	secret := resp.SecretBundle.Value
	if secret != nil {
		showValue(*secret)
	}
}

type secretVersion struct {
	Id      string
	Created *time.Time
}

// func getLatestSecretVersionId(secretName string, client *azsecrets.Client) (string, error) {
// 	versions, err := getSecretVersions(secretName, client)
// 	if err != nil {
// 		return "", errors.Wrapf(err, "failed to get secret versions")
// 	}

// 	return getLatestSecretVersionIdFromVersions(versions)
// }

// func getLatestSecretVersionIdFromVersions(versions []secretVersion) (string, error) {

// 	sortSecretVersionsNewestFirst(versions)
// 	return versions[0].Id, nil
// }

// sortSecretVersionsNewestFirst sorts a COPY and returns it
func sortSecretVersionsNewestFirst(versions []secretVersion) []secretVersion {
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].Created == nil || versions[j].Created == nil {
			return false
		}

		// We return the inverse of "less", because we want descending order
		return !versions[i].Created.Before(*versions[j].Created)
	})

	return versions
}

func getSecretVersions(secretName string, client *azsecrets.Client) ([]secretVersion, error) {
	pager := client.NewListSecretVersionsPager(secretName, nil)

	versions := []secretVersion{}

	for pager.More() {
		resp, err := pager.NextPage(context.Background())
		if err != nil {
			return nil, errors.Wrap(err, "failed to get paged secret versions")
		}

		for _, v := range resp.Value {
			versions = append(versions, secretVersion{Id: v.ID.Version(), Created: v.Attributes.Created})
		}
	}

	return versions, nil
}

func copySecret() {
	clipboard.WriteAll(secretValue.GetText(true))
}

// func vaultModal() {
//     modal := tview.NewModal().Set
// }
