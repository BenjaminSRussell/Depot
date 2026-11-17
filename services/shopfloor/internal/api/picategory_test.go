package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/store"
)

const piCategoryID = "0195f0a0-0000-7000-8000-0000000000c1"

func (f *fakeStore) CreatePICategory(_ context.Context, in store.PICategoryInput, by string, _ store.Idem) (store.PICategory, error) {
	f.piCategoryIn = &in
	return store.PICategory{ID: piCategoryID, Code: in.Code, Name: in.Name, Description: in.Description, Active: in.Active, Version: 1, CreatedBy: by}, nil
}

func (f *fakeStore) UpdatePICategory(_ context.Context, id string, version int, in store.PICategoryInput, by string) (store.PICategory, error) {
	f.piCategoryIn, f.updateVersion = &in, version
	return store.PICategory{ID: id, Code: "WEIGH", Name: in.Name, Active: in.Active, Version: version + 1, CreatedBy: by}, nil
}

func (f *fakeStore) GetPICategory(_ context.Context, id string) (store.PICategory, error) {
	return store.PICategory{ID: id, Code: "WEIGH", Name: "Weighing", Active: true, Version: 2}, nil
}

func (f *fakeStore) ListPICategories(context.Context, bool, int) ([]store.PICategory, error) {
	return []store.PICategory{{ID: piCategoryID, Code: "WEIGH", Name: "Weighing", Active: true, Version: 1}}, nil
}

func TestPICategory_CreateUpdateAndRules(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	path := "/v1/shopfloor/pi-categories"
	if rec := call(t, h, http.MethodPost, path, `{"code":"WEIGH","name":"Weighing"}`, DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("create without the maintain duty: %d", rec.Code)
	}
	rec := call(t, h, http.MethodPost, path, `{"code":"WEIGH","name":"Weighing","description":"Scale steps"}`, DutyPICategoryMaintain)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || f.piCategoryIn == nil || !f.piCategoryIn.Active || f.piCategoryIn.Description != "Scale steps" {
		t.Fatalf("create: %d %s %+v", rec.Code, rec.Body.String(), f.piCategoryIn)
	}
	for name, body := range map[string]string{
		"lower-case code": `{"code":"weigh","name":"A"}`,
		"missing code":    `{"name":"A"}`,
		"empty name":      `{"code":"A","name":" "}`,
		"unknown field":   `{"code":"A","name":"A","extra":1}`,
	} {
		if rec := call(t, h, http.MethodPost, path, body, DutyPICategoryMaintain); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want a validation error", name, rec.Code)
		}
	}
	one := path + "/" + piCategoryID
	if rec := callWith(t, h, http.MethodPut, one, `{"name":"Weighing"}`, DutyPICategoryMaintain, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	rec = callWith(t, h, http.MethodPut, one, `{"name":"Weighing","active":false}`, DutyPICategoryMaintain, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || f.updateVersion != 2 || f.piCategoryIn.Active || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if rec := callWith(t, h, http.MethodPut, path+"/x", `{"name":"W"}`, DutyPICategoryMaintain, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodePICategoryNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestPICategory_ReadsAndDuties(t *testing.T) {
	h := newRouter(&fakeStore{})
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/pi-categories?active=true", "", DutyView); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/pi-categories/"+piCategoryID, "", DutyView); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/pi-categories", "", DutyPICategoryMaintain); rec.Code != http.StatusForbidden {
		t.Fatalf("list needs the view duty: %d", rec.Code)
	}
}
