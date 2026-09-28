package rbac

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The RBAC registration tests (TestAddTools, TestAddToolsReadOnly) were
// deleted with the AddTools method: registration is asserted once, by the
// merged package's 7-tool / 5-tool invariants. What remains here are the
// fixtures the handler tests share.

const (
	fakeURL   = "https://localhost:8080"
	fakeToken = "fakeToken"
)

var rbacGVRs = map[schema.GroupVersionResource]string{
	{Group: "management.cattle.io", Version: "v3", Resource: "clusterroletemplatebindings"}: "ClusterRoleTemplateBindingList",
	{Group: "management.cattle.io", Version: "v3", Resource: "projectroletemplatebindings"}: "ProjectRoleTemplateBindingList",
	{Group: "management.cattle.io", Version: "v3", Resource: "roletemplates"}:               "RoleTemplateList",
	{Group: "management.cattle.io", Version: "v3", Resource: "users"}:                       "UserList",
}

func rbacScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = metav1.AddMetaToScheme(scheme)
	return scheme
}
