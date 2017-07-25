package watchers

import (
	"reflect"
	"time"
	"github.com/golang/glog"
	"k8s.io/client-go/kubernetes"
	api "k8s.io/client-go/pkg/api/v1"
	cache "k8s.io/client-go/tools/cache"
	"k8s.io/apimachinery/pkg/fields"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kube-enn-proxy/pkg/watchers/config"

)


// EndpointsUpdate describes an operation of endpoints,
// You can add, update or remove single endpoints by setting Op == ADD|UPDATE|REMOVE.
type EndpointsUpdate struct {
	Endpoints *api.Endpoints
	Op        util.Operation
}

type EndpointsWatcher struct {
	clientset           *kubernetes.Clientset
	endpointsController cache.Controller
	endpointsLister     cache.Indexer
	broadcaster         *util.Broadcaster
	StopCh              chan struct{}
}

var EndpointsWatchConfig *EndpointsWatcher

// EndpointsUpdatesHandler is an abstract interface of objects which receive update notifications for the set of endpoints.
type EndpointsUpdatesHandler interface {
	OnEndpointsUpdate(endpointsUpdate *EndpointsUpdate)
}


func (ew *EndpointsWatcher) endpointsAdd(obj interface{}){
	endpoints, ok := obj.(*api.Endpoints)
	if !ok {
		glog.Errorf("endpointsAdd: failed")
		return
	}
	ew.broadcaster.Notify(&EndpointsUpdate{
		Op:        util.ADD,
		Endpoints: endpoints,
	})
}

func (ew *EndpointsWatcher) endpointsDelete(obj interface{}){
	endpoints, ok := obj.(*api.Endpoints)
	if !ok {
		glog.Errorf("endpointsDelete: failed")
		return
	}
	ew.broadcaster.Notify(&EndpointsUpdate{
		Op:        util.REMOVE,
		Endpoints: endpoints,
	})
}

func (ew *EndpointsWatcher) endpointsUpdate(old, obj interface{}){
	endpoints_new, ok := obj.(*api.Endpoints)
	if !ok {
		glog.Errorf("endpointsUpdate: new obj failed")
		return
	}

	_, ok = obj.(*api.Endpoints)
	if !ok {
		glog.Errorf("endpointsUpdate: old obj failed")
		return
	}

	if !reflect.DeepEqual(old, obj){
		ew.broadcaster.Notify(&EndpointsUpdate{
			Op:        util.UPDATE,
			Endpoints: endpoints_new,
		})
	}

}

func (ew *EndpointsWatcher) RegisterHandler(handler EndpointsUpdatesHandler) {
	ew.broadcaster.Add(util.ListenerFunc(func(instance interface{}) {
		glog.Infof("RegisterHandler: Calling handler.OnEndpointsUpdate()")
		handler.OnEndpointsUpdate(instance.(*EndpointsUpdate))
	}))
}

func (ew *EndpointsWatcher) HasSynced() bool {
	return ew.endpointsController.HasSynced()
}

func (ew *EndpointsWatcher) List() []*api.Endpoints {
	list := ew.endpointsLister.List()
	var endpoints_list []*api.Endpoints
	endpoints_list = make([]*api.Endpoints, len(list))
	for i, enp := range list{
		endpoints_list[i] = enp.(*api.Endpoints)
	}
	return endpoints_list
}

func NewEndpointWatcher (
	clientset *kubernetes.Clientset,
	resyncPeriod time.Duration,
) (*EndpointsWatcher, error) {
	ew := EndpointsWatcher{
		clientset:   clientset,
		broadcaster: util.NewBroadcaster(),
		StopCh:      make(chan struct{}),
	}

	lw := cache.NewListWatchFromClient(clientset.Core().RESTClient(), "endpoints", metav1.NamespaceAll, fields.Everything())

	eventHandler := cache.ResourceEventHandlerFuncs{
		AddFunc:    ew.endpointsAdd,
		DeleteFunc: ew.endpointsDelete,
		UpdateFunc: ew.endpointsUpdate,
	}

	ew.endpointsLister, ew.endpointsController = cache.NewIndexerInformer(
		lw,
		&api.Endpoints{},
		resyncPeriod,
		eventHandler,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
		},
	)

	EndpointsWatchConfig = &ew

	go ew.endpointsController.Run(ew.StopCh)

	return &ew, nil
}

func (ew *EndpointsWatcher) StopEndpointsWatcher() {
	ew.StopCh <- struct{}{}
}