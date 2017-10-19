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

// ServicesUpdate describes an operation of service,
// You can add, update or remove single endpoints by setting Op == ADD|UPDATE|REMOVE.
type ServicesUpdate struct {
	Service   *api.Service
	Op        util.Operation
}

type ServicesWatcher struct {
	clientset           *kubernetes.Clientset
	servicesController  cache.Controller
	servicesLister      cache.Indexer
	broadcaster         *util.Broadcaster
	StopCh              chan struct{}
}

var ServiceWatchConfig *ServicesWatcher

// ServicesUpdatesHandler is an abstract interface of objects which receive update notifications for the set of endpoints.
type ServicesUpdatesHandler interface {
	OnServicesUpdate(servicesUpdate *ServicesUpdate)
}


func (sw *ServicesWatcher) servicesAdd(obj interface{}){
	services, ok := obj.(*api.Service)
	if !ok {
		glog.Errorf("servicesAdd: failed")
		return
	}
	sw.broadcaster.Notify(&ServicesUpdate{
		Op:        util.ADD,
		Service:   services,
	})
}

func (sw *ServicesWatcher) servicesDelete(obj interface{}){
	services, ok := obj.(*api.Service)
	if !ok {
		glog.Errorf("servicesDelete: failed")
		return
	}
	sw.broadcaster.Notify(&ServicesUpdate{
		Op:        util.REMOVE,
		Service:   services,
	})
}

func (sw *ServicesWatcher) servicesUpdate(old, obj interface{}){
	services_new, ok := obj.(*api.Service)
	if !ok {
		glog.Errorf("servicesUpdate: new obj failed")
		return
	}

	_, ok = obj.(*api.Service)
	if !ok {
		glog.Errorf("servicesUpdate: old obj failed")
		return
	}

	if !reflect.DeepEqual(old, obj){
		sw.broadcaster.Notify(&ServicesUpdate{
			Op:        util.UPDATE,
			Service:   services_new,
		})
	}

}

func (sw *ServicesWatcher) RegisterHandler(handler ServicesUpdatesHandler) {
	sw.broadcaster.Add(util.ListenerFunc(func(instance interface{}) {
		glog.V(2).Infof("RegisterHandler: Calling handler.OnServicesUpdate()")
		handler.OnServicesUpdate(instance.(*ServicesUpdate))
	}))
}

func (sw *ServicesWatcher) HasSynced() bool {
	return sw.servicesController.HasSynced()
}

func (sw *ServicesWatcher) List() []*api.Service {
	list := sw.servicesLister.List()
	var service_list []*api.Service
	service_list = make([]*api.Service, len(list))
	for i, svc := range list{
		service_list[i] = svc.(*api.Service)
	}
	return service_list
}

func NewServiceWatcher (
	clientset *kubernetes.Clientset,
	resyncPeriod time.Duration,
)(*ServicesWatcher, error) {
	sw := ServicesWatcher{
		clientset:   clientset,
		broadcaster: util.NewBroadcaster(),
		StopCh:      make(chan struct{}),
	}

	lw := cache.NewListWatchFromClient(clientset.Core().RESTClient(), "services", metav1.NamespaceAll, fields.Everything())

	eventHandler := cache.ResourceEventHandlerFuncs{
		AddFunc:    sw.servicesAdd,
		DeleteFunc: sw.servicesDelete,
		UpdateFunc: sw.servicesUpdate,
	}

	sw.servicesLister, sw.servicesController = cache.NewIndexerInformer(
		lw,
		&api.Service{},
		resyncPeriod,
		eventHandler,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
		},
	)

	ServiceWatchConfig = &sw

	go sw.servicesController.Run(sw.StopCh)

	return &sw, nil
}

func (sw *ServicesWatcher) StopServiceWatcher() {
	sw.StopCh <- struct{}{}
}

