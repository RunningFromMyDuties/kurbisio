package backend

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/joeshaw/envdecode"
	"github.com/relabs-tech/backends/core"
	"github.com/relabs-tech/backends/core/access"

	"github.com/relabs-tech/backends/core/client"
	"github.com/relabs-tech/backends/core/sql"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
)

var configurationJSON string = `{
	"collections": [
	  {
		"resource": "a",
		"external_index": "external_id",
		"static_properties": ["static_prop"],
		"searchable_properties": ["searchable_prop"]
	  },
	  {
		"resource": "b"
	  },
	  {
		"resource": "b/c"
	  },
	  {
		"resource": "b/c/d"
	  },
	  {
		"resource": "o"
	  },
	  {
		"resource": "created_time"
	  },
	  {
		"resource": "state"
	  },
	  {
		"resource":"notification"
	  },
	  {
		"resource":"notification/normal"
	  }
	],
	"singletons": [
	  {
		"resource": "o/s"
	  },
	  {
		"resource":"notification/single"
	  }
	],
	"blobs": [
	  {
		"resource": "blob",
		"searchable_properties":["content_type"],
		"mutable": true
	  }
	],
	"relations": [],
	"shortcuts": [
		{
			"shortcut" : "b",
			"target" : "b",
			"roles" : ["beerole"]
		}
	]
  }
`

// TestService holds the configuration for this service
//
// use POSTGRES="host=localhost port=5432 user=postgres password=docker dbname=postgres sslmode=disable"
type TestService struct {
	Postgres string `env:"POSTGRES,required" description:"the connection string for the Postgres DB"`
	backend  *Backend
	client   client.Client
}

var testService TestService

func TestMain(m *testing.M) {
	if err := envdecode.Decode(&testService); err != nil {
		panic(err)
	}

	db := sql.OpenWithSchema(testService.Postgres, "_core_unit_test_")
	defer db.Close()
	db.ClearSchema()

	router := mux.NewRouter()
	testService.backend = New(&Builder{
		Config: configurationJSON,
		DB:     db,
		Router: router,
	})
	testService.client = client.New(router)

	code := m.Run()
	os.Exit(code)
}

func TestCollectionA(t *testing.T) {

	type A struct {
		AID            uuid.UUID         `json:"a_id"`
		Properties     map[string]string `json:"properties"`
		ExternalID     string            `json:"external_id"`
		StaticProp     string            `json:"static_prop"`
		SearchableProp string            `json:"searchable_prop"`
		CreatedAt      time.Time         `json:"created_at"`
	}

	someJSON := map[string]string{
		"foo": "bar",
	}

	aNew := A{
		Properties:     someJSON,
		ExternalID:     "external",
		StaticProp:     "static",
		SearchableProp: "searchable",
		CreatedAt:      time.Now().UTC().Round(time.Millisecond), // round to postgres precision
	}

	a := A{}

	_, err := testService.client.Post("/as", &aNew, &a)
	if err != nil {
		t.Fatal(err)
	}

	u := uuid.UUID{}
	if a.AID == u {
		t.Fatal("no id")
	}

	if asJSON(a.Properties) != asJSON(aNew.Properties) ||
		a.ExternalID != aNew.ExternalID ||
		a.StaticProp != aNew.StaticProp ||
		a.SearchableProp != aNew.SearchableProp ||
		a.CreatedAt != aNew.CreatedAt {
		t.Fatal("unexpected result:", asJSON(a), "expected:", asJSON(aNew))
	}

	aGet := A{}
	_, err = testService.client.Get("/as/"+a.AID.String(), &aGet)
	if err != nil {
		t.Fatal(err)
	}
	if asJSON(a.Properties) != asJSON(aGet.Properties) ||
		a.ExternalID != aGet.ExternalID ||
		a.StaticProp != aGet.StaticProp ||
		a.CreatedAt != aGet.CreatedAt {
		t.Fatal("unexpected result:", asJSON(aGet))
	}

	aPut := aGet
	aRes := A{}
	aPut.StaticProp = "new value for static property"
	_, err = testService.client.Put("/as", &aPut, &aRes)
	if err != nil {
		t.Fatal(err)
	}
	if asJSON(aPut.Properties) != asJSON(aRes.Properties) ||
		aPut.ExternalID != aRes.ExternalID ||
		aPut.StaticProp != aRes.StaticProp ||
		aPut.CreatedAt != aRes.CreatedAt {
		t.Fatal("unexpected result:", asJSON(aGet))
	}

	_, err = testService.client.Delete("/as/" + a.AID.String())
	if err != nil {
		t.Fatal(err)
	}
	status, err := testService.client.Get("/as/"+a.AID.String(), &aGet)
	if status != http.StatusNotFound {
		t.Fatal("not deleted")
	}
}

type Empty struct{}

type B struct {
	BID uuid.UUID `json:"b_id"`
}

type C struct {
	B
	CID uuid.UUID `json:"c_id"`
}

type D struct {
	C
	DID uuid.UUID `json:"d_id"`
}

func TestResourceBCD(t *testing.T) {

	empty := Empty{}
	b := B{}

	_, err := testService.client.Post("/bs", &empty, &b)
	if err != nil {
		t.Fatal(err)
	}

	c := C{}
	_, err = testService.client.Post("/bs/"+b.BID.String()+"/cs", &empty, &c)
	if err != nil {
		t.Fatal(err)
	}

	d := D{}
	_, err = testService.client.Post("/bs/"+b.BID.String()+"/cs/"+c.CID.String()+"/ds", &empty, &d)
	if err != nil {
		t.Fatal(err)
	}

	if d.BID != b.BID || d.CID != c.CID {
		t.Fatal("properties do not match:", asJSON(d))
	}

	// delete the root object b, this should cascade to all child objects
	status, err := testService.client.Delete("/bs/" + b.BID.String())
	if status != http.StatusNoContent {
		t.Fatal("delete failed")
	}
	if err != nil {
		t.Fatal(err)
	}
	// cross check that the cascade worked: deleting b has also deleted c and d
	dGet := D{}
	status, err = testService.client.Get("/bs/"+b.BID.String()+"/cs/"+c.CID.String()+"/ds/"+d.DID.String(), &dGet)
	if status != http.StatusNotFound {
		t.Fatal("cascade delete failed")
		if err != nil {
			t.Fatal(err)
		}
	}

}

func TestResourceBCD_Shortcuts(t *testing.T) {

	empty := Empty{}
	b := B{}

	_, err := testService.client.Post("/bs", &empty, &b)
	if err != nil {
		t.Fatal(err)
	}

	auth := access.Authorization{
		Roles:     []string{"beerole"},
		Selectors: map[string]string{"b_id": b.BID.String()},
	}

	authenticatedClient := testService.client.WithAuthorization(&auth)

	bl := B{}
	_, err = authenticatedClient.Get("/b", &bl)
	if err != nil {
		t.Fatal(err)
	}
	if bl.BID != b.BID {
		t.Fatal("id does not match:", asJSON(bl))
	}

	c := C{}
	_, err = authenticatedClient.Post("/b/cs", &empty, &c)
	if err != nil {
		t.Fatal(err)
	}

	d := D{}
	_, err = authenticatedClient.Post("/b/cs/"+c.CID.String()+"/ds", &empty, &d)
	if err != nil {
		t.Fatal(err)
	}

	if d.BID != b.BID || d.CID != c.CID {
		t.Fatal("properties do not match:", asJSON(d))
	}

	// delete the root object b, this should cascade to all child objects
	status, err := authenticatedClient.Delete("/b")
	if status != http.StatusNoContent {
		t.Fatal("delete failed")
	}
	if err != nil {
		t.Fatal(err)
	}
	// cross check that the cascade worked: deleting b has also deleted c and d
	dGet := D{}
	status, err = testService.client.Get("/bs/"+b.BID.String()+"/cs/"+c.CID.String()+"/ds/"+d.DID.String(), &dGet)
	if status != http.StatusNotFound {
		t.Fatal("cascade delete failed")
		if err != nil {
			t.Fatal(err)
		}
	}

}

func TestSingletonOS(t *testing.T) {
	type O struct {
		OID uuid.UUID `json:"o_id"`
	}

	type S struct {
		SID        uuid.UUID         `json:"s_id"`
		Properties map[string]string `json:"properties"`
	}

	empty := Empty{}

	// create o instance
	o := O{}
	_, err := testService.client.Post("/os", &empty, &o)
	if err != nil {
		t.Fatal(err)
	}

	// create single s with initial name
	s := S{
		Properties: map[string]string{
			"name": "initial",
		},
	}
	sResult := S{}
	_, err = testService.client.Put("/os/"+o.OID.String()+"/s", &s, &sResult)
	if err != nil {
		t.Fatal(err)
	}

	if name, ok := sResult.Properties["name"]; !ok || name != "initial" {
		t.Fatal("properties not as expected:", asJSON(sResult))
	}

	// update single s to have updated name, the object's id (sid) remains the same
	sUpdate := S{
		Properties: map[string]string{
			"name": "updated",
		},
	}
	sUpdateResult := S{}

	status, err := testService.client.Put("/os/"+o.OID.String()+"/s", &sUpdate, &sUpdateResult)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := sUpdateResult.Properties["name"]; !ok || name != "updated" {
		t.Fatal("properties not as expected:", asJSON(sUpdateResult))
	}
	if sUpdateResult.SID != sResult.SID {
		t.Fatal("got a new object, should have gotten the same object")
	}

	oldUID := sResult.SID
	newUID := uuid.New()

	// put another update to s and try to give it a new id. This will ingore the new
	// uid and simply update the rest of the object
	sUpdate.SID = newUID
	status, err = testService.client.Put("/os/"+o.OID.String()+"/s", &sUpdate, &sUpdateResult)
	if err != nil {
		t.Fatal(err)
	}

	if sUpdateResult.SID != oldUID {
		t.Fatal("singleton id changed")
	}

	// delete single s
	status, err = testService.client.Delete("/os/" + o.OID.String() + "/s")
	if status != http.StatusNoContent {
		t.Fatal("delete failed")
	}
	if err != nil {
		t.Fatal(err)
	}

	// cross check that the delete worked
	sGet := S{}
	status, err = testService.client.Get("/os/"+o.OID.String()+"/s", &sGet)
	if status != http.StatusNoContent {
		t.Fatal("delete failed")
		if err != nil {
			t.Fatal(err)
		}
	}

	// re-create single s with new uuid, now the sid should be the new one we set
	sResult2 := S{}

	s.SID = newUID
	_, err = testService.client.Put("/os/"+o.OID.String()+"/s", &s, &sResult2)
	if err != nil {
		t.Fatal(err)
	}
	if sResult2.SID == sResult.SID {
		t.Fatal("recreation did not work, still same ID")
	}
	if sResult2.SID != newUID {
		t.Fatal("recreation did not work, could not choose ID")
	}

	// delete the owner o, this should also delete the single s
	status, err = testService.client.Delete("/os/" + o.OID.String())
	if status != http.StatusNoContent {
		t.Fatal("delete failed")
	}
	if err != nil {
		t.Fatal(err)
	}

	// cross check that the cascade worked: deleting o has also deleted s
	status, err = testService.client.Get("/os/"+o.OID.String()+"/s", &sGet)
	if status != http.StatusNoContent {
		t.Fatal("cascade delete failed")
		if err != nil {
			t.Fatal(err)
		}
	}

}

func TestCreatedTimeAndNullID(t *testing.T) {

	type CreatedTime struct {
		CreatedTimeID uuid.UUID `json:"created_time_id"`
		CreatedAt     time.Time `json:"created_at"`
	}

	now := time.Now().UTC().Round(time.Millisecond) // round to postgres precision
	cNew := CreatedTime{CreatedAt: now}
	var c CreatedTime
	_, err := testService.client.Post("/created_times", &cNew, &c)
	if err != nil {
		t.Fatal(err)
	}
	if c.CreatedTimeID == cNew.CreatedTimeID {
		t.Fatal("null id was not replaced")
	}
	if c.CreatedAt != cNew.CreatedAt {
		t.Fatal("created_at was not kept")
	}

	// an empty created_at string should produce an error
	emptyString := struct {
		CreatedAt string `json:"created_at"`
	}{
		CreatedAt: "",
	}
	_, err = testService.client.Post("/created_times", &emptyString, &c)
	if err == nil {
		t.Fatal("eerror expected")
	}

}

func TestState(t *testing.T) {
	type State struct {
		StateID uuid.UUID `json:"state_id"`
		State   string    `json:"state"`
	}

	state := State{
		State: "partial",
	}
	var h State
	_, err := testService.client.Post("/states", &state, &h)
	if err != nil {
		t.Fatal(err)
	}

	var collection []State
	_, err = testService.client.Get("/states", &collection)
	if err != nil {
		t.Fatal(err)
	}
	if len(collection) != 0 {
		t.Fatal("collection not empty as expected")
	}

	//  the item should be visible in the collection with the state query parameter
	_, err = testService.client.Get("/states?state=partial", &collection)
	if err != nil {
		t.Fatal(err)
	}
	if len(collection) == 0 {
		t.Fatal("collection is empty, unexpected")
	}

	// create a visible item
	visible := State{
		State: "",
	}
	var v State
	_, err = testService.client.Post("/states", &visible, &v)
	if err != nil {
		t.Fatal(err)
	}

	// we should now have one visible and one state item
	_, err = testService.client.Get("/states", &collection)
	if err != nil {
		t.Fatal(err)
	}
	if len(collection) != 1 {
		t.Fatal("visible collection does not have one item as expected")
	}
	_, err = testService.client.Get("/states?state=partial", &collection)
	if err != nil {
		t.Fatal(err)
	}
	if len(collection) != 1 {
		t.Fatal("state collection does not have one item as expected")
	}

	// lets make the state item visible
	h.State = ""
	var h3 State
	_, err = testService.client.Put("/states", &h, &h3)
	if err != nil {
		t.Fatal(err)
	}
	if h3.State != "" {
		t.Fatal("still state not empty, but should be empty")
	}

	// now the item should be visible in the collection, hence we have two items there
	_, err = testService.client.Get("/states", &collection)
	if err != nil {
		t.Fatal(err)
	}
	if len(collection) != 2 {
		t.Fatal("collection does not have two items, unexpected")
	}
}

func TestBlob(t *testing.T) {

	type Blob struct {
		BlobID      uuid.UUID `json:"blob_id"`
		CreatedAt   time.Time `json:"created_at"`
		ContentType string    `json:"content_type"`
	}

	data, err := ioutil.ReadFile("./testdata/dalarubettrich.png")
	if err != nil {
		t.Fatal(err)
	}

	var br Blob
	header := map[string]string{
		"Content-Type":       "image/png",
		"Kurbisio-Meta-Data": `{"hello":"world"}`,
	}
	_, err = testService.client.PostBlob("/blobs", header, data, &br)
	if err != nil {
		t.Fatal(err)
	}

	list := []Blob{}
	_, err = testService.client.Get("/blobs", &list)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("unexpected blob list size %d", len(list))
	}
	if list[0].BlobID != br.BlobID {
		t.Fatal("missing my blob")
	}
	if list[0].ContentType != "image/png" {
		t.Fatal("wrong content type")
	}

	var dataReturn []byte
	_, headerReturn, err := testService.client.BlobWithHeader("/blobs/"+br.BlobID.String(), &dataReturn)

	if err != nil {
		t.Fatal(err)
	}
	if headerReturn.Get("Content-Type") != header["Content-Type"] {
		t.Fatal("wrong Content-Type in return header")
	}

	if headerReturn.Get("Kurbisio-Meta-Data") != header["Kurbisio-Meta-Data"] {
		t.Fatal("wrong meta data in return header")
	}

	if bytes.Compare(data, dataReturn) != 0 {
		t.Fatal("returned binary data is not equal")
	}

	// now update the  blob with something completely different
	var ubr Blob
	uHeader := map[string]string{
		"Content-Type": "something weird",
	}
	uData := []byte("binary stuff")
	_, err = testService.client.PutBlob("/blobs/"+br.BlobID.String(), uHeader, uData, &ubr)
	if err != nil {
		t.Fatal(err)
	}

	var uDataReturn []byte
	_, uHeaderReturn, err := testService.client.BlobWithHeader("/blobs/"+br.BlobID.String(), &uDataReturn)

	if err != nil {
		t.Fatal(err)
	}
	if uHeaderReturn.Get("Content-Type") != uHeader["Content-Type"] {
		t.Fatal("wrong Content-Type in return header")
	}

	if uHeaderReturn.Get("Kurbisio-Meta-Data") != "{}" {
		t.Fatal("got meta data, but should have been cleared")
	}

	if bytes.Compare(uData, uDataReturn) != 0 {
		t.Fatal("returned binary data is not equal")
	}

}

func TestNotifications(t *testing.T) {

	var (
		createCount, updateCount, deleteCount, pointsCount int
	)
	createHandler := func(n Notification) bool {
		createCount++
		return false
	}
	updateHandler := func(n Notification) bool {
		updateCount++
		var object map[string]interface{}
		err := json.Unmarshal(n.Payload, &object)
		if err != nil {
			t.Fatal(err)
		}
		if properties, ok := object["properties"].(map[string]interface{}); ok {
			if points, ok := properties["points"].(float64); ok {
				pointsCount += int(points)
			}
		}
		return false
	}
	deleteHandler := func(n Notification) bool {
		deleteCount++
		return false
	}

	backend := testService.backend

	backend.RequestNotification("notification", core.OperationCreate, "mystate", &createHandler)
	backend.RequestNotification("notification/normal", core.OperationCreate, "mystate", &createHandler)
	backend.RequestNotification("notification/single", core.OperationCreate, "mystate", &createHandler)

	backend.RequestNotification("notification", core.OperationUpdate, "mystate", &updateHandler)
	backend.RequestNotification("notification/normal", core.OperationUpdate, "mystate", &updateHandler)
	backend.RequestNotification("notification/single", core.OperationUpdate, "mystate", &updateHandler)

	backend.RequestNotification("notification", core.OperationDelete, "mystate", &deleteHandler)
	backend.RequestNotification("notification/normal", core.OperationDelete, "mystate", &deleteHandler)
	backend.RequestNotification("notification/single", core.OperationDelete, "mystate", &deleteHandler)

	client := testService.client

	// create root object
	type G map[string]interface{}
	nreq := G{"state": "mystate"}
	var nres G
	_, err := client.Post("/notifications", &nreq, &nres)
	if err != nil {
		t.Fatal(err)
	}
	nid, _ := nres["notification_id"].(string)

	// update root object with 1 point
	nres["properties"] = map[string]int64{"points": 1}
	_, err = client.Put("/notifications", &nres, &nres)
	if err != nil {
		t.Fatal(err)
	}

	// create child collection object
	nnreq := G{"state": "mystate", "notification_id": nid}
	var nnres G
	_, err = client.Post("/notifications/"+nid+"/normals", &nnreq, &nnres)
	if err != nil {
		t.Fatal(err)
	}

	// update child collection object with 5 points
	nnres["properties"] = map[string]int64{"points": 5}
	_, err = client.Put("/notifications/"+nid+"/normals", &nnres, &nnres)
	if err != nil {
		t.Fatal(err)
	}

	// delete child collection object
	_, err = client.Delete("/notifications/" + nid + "/normals/" + nnres["normal_id"].(string))
	if err != nil {
		t.Fatal(err)
	}

	// create child singleton object with collection path
	nsreq := G{"state": "mystate", "notification_id": nid}
	var nsres G
	_, err = client.Post("/notifications/"+nid+"/singles", &nsreq, &nsres)
	if err != nil {
		t.Fatal(err)
	}

	// update child collection object with collection path and 2 points
	nsres["properties"] = map[string]int64{"points": 2}
	_, err = client.Put("/notifications/"+nid+"/singles", &nsres, &nsres)
	if err != nil {
		t.Fatal(err)
	}

	// delete child singleton object with wildcard path
	_, err = client.Delete("/notifications/all/singles/" + nsres["single_id"].(string))
	if err != nil {
		t.Fatal(err)
	}

	// re-create child singleton object with singleton path
	nsreq = G{"state": "mystate"}
	_, err = client.Put("/notifications/"+nid+"/single", &nsreq, &nsres)
	if err != nil {
		t.Fatal(err)
	}

	// update child collection object with singleton path and 3 points
	nsres["properties"] = map[string]int64{"points": 3}
	_, err = client.Put("/notifications/"+nid+"/single", &nsres, &nsres)
	if err != nil {
		t.Fatal(err)
	}

	// delete child singleton object with singleton path
	_, err = client.Delete("/notifications/" + nid + "/single")
	if err != nil {
		t.Fatal(err)
	}

	// delete root object
	_, err = client.Delete("/notifications/" + nid)
	if err != nil {
		t.Fatal(err)
	}

	if createCount != 4 {
		t.Fatalf("unexpected number of creates: %d", createCount)
	}
	if updateCount != 4 {
		t.Fatalf("unexpected number of updates: %d", updateCount)
	}
	if deleteCount != 4 {
		t.Fatalf("unexpected number of deletes: %d", deleteCount)
	}
	if pointsCount != 11 {
		t.Fatalf("unexpected number of points: %d", pointsCount)
	}

	removed := backend.RemoveNotificationHandler(&createHandler)
	removed += backend.RemoveNotificationHandler(&updateHandler)
	removed += backend.RemoveNotificationHandler(&deleteHandler)
	if removed != 9 {
		t.Fatalf("wrong number of removed handlers. Expected 9 but got %d", removed)
	}
}

func asJSON(object interface{}) string {
	j, _ := json.MarshalIndent(object, "", "  ")
	return string(j)
}
