package v1alpha1

import (
	"reflect"
	"strconv"

	"github.com/mariomac/gostream/stream"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/grafana/ebpf-autoinstrument-operator/pkg/helper"

	v1 "k8s.io/api/core/v1"
)

// TODO: user-overridable
const (
	instrumenterName            = "grafana-ebpf-autoinstrumenter"
	instrumenterImage           = "grafana/ebpf-autoinstrument:latest"
	instrumenterImagePullPolicy = "Always"

	InstrumentedLabel = "grafana.com/instrumented-by"
)

var log = logf.Log.WithName("sidecar-instrumenter")

// NeedsInstrumentation returns whether the given pod requires instrumentation,
// and a container with the instrumenter, in case of requiring it.
func NeedsInstrumentation(iq *Instrumenter, dst *v1.Pod) (*v1.Container, bool) {
	if dst.Labels == nil {
		return nil, false
	}
	// if the Pod does not have the port selection label,
	// or it's being already instrumented by another Instrumenter
	if dst.Labels[iq.Spec.Selector.PortLabel] == "" ||
		(dst.Labels[InstrumentedLabel] != "" && dst.Labels[InstrumentedLabel] != iq.Name) {
		return nil, false
	}
	expected := buildSidecar(iq, dst)
	actual, ok := findByName(dst.Spec.Containers)
	if !ok {
		return expected, true
	}
	if reflect.DeepEqual(expected, actual) {
		return nil, false
	}
	return expected, true
}

// InstrumentIfRequired instruments, if needed, the destination pod, and returns whether it has been instrumented
func InstrumentIfRequired(iq *Instrumenter, dst *v1.Pod) bool {
	sidecar, ok := NeedsInstrumentation(iq, dst)
	if !ok {
		return false
	}
	AddInstrumenter(iq.Name, sidecar, dst)
	return true
}

func AddInstrumenter(instrumenterName string, sidecar *v1.Container, dst *v1.Pod) {
	// it might happen that the sidecar container needs to be replaced or added
	current, ok := findByName(dst.Spec.Containers)
	if ok {
		*current = *sidecar
	} else {
		dst.Spec.Containers = append(dst.Spec.Containers, *sidecar)
	}
	labelInstrumented(instrumenterName, dst)
	// TODO: on Pod recreation, restore the previous value of this property (e.g. store it in an annotation)
	dst.Spec.ShareProcessNamespace = helper.Ptr(true)
}

func RemoveInstrumenter(dst *v1.Pod) {
	unlabelInstrumented(dst)
	dst.Spec.Containers = stream.OfSlice(dst.Spec.Containers).
		Filter(func(c v1.Container) bool {
			return c.Name != instrumenterName
		}).ToSlice()
}

func buildSidecar(iq *Instrumenter, dst *v1.Pod) *v1.Container {
	lbls := dst.ObjectMeta.Labels
	log.Info("labels", "labels", lbls, "query", iq)

	// TODO: extract this information from owner (daemonset, deployment, replicaset...)
	svcName, svcNamespace := dst.Name, dst.Namespace

	// TODO: do not make pod failing if sidecar fails, just report it in the Instrumenter status
	sidecar := &v1.Container{
		Name:            instrumenterName,
		Image:           instrumenterImage,
		ImagePullPolicy: instrumenterImagePullPolicy,
		// TODO: capabilities by default, or privileged only if user requests for it
		SecurityContext: &v1.SecurityContext{
			Privileged: helper.Ptr(true),
			RunAsUser:  helper.Ptr(int64(0)),
		},
		Env: []v1.EnvVar{
			{Name: "SERVICE_NAME", Value: svcName},
			{Name: "SERVICE_NAMESPACE", Value: svcNamespace},
			// TODO: use only in debug mode
			{Name: "PRINT_TRACES", Value: "true"},
			{Name: "OPEN_PORT", Value: lbls[iq.Spec.Selector.PortLabel]},
		},
	}
	exporters := map[Exporter]struct{}{}
	for _, e := range iq.Spec.Export {
		exporters[e] = struct{}{}
	}
	if _, ok := exporters[ExporterPrometheus]; ok {
		configurePrometheusExporter(svcName, iq, sidecar)
	}
	if _, ok := exporters[ExporterOTELMetrics]; ok {
		log.Info("exporter " + ExporterOTELMetrics + " not yet available. Ignoring")
	}
	if _, ok := exporters[ExporterOTELTraces]; ok {
		log.Info("exporter " + ExporterOTELTraces + " not yet available. Ignoring")
	}

	return sidecar
}

func configurePrometheusExporter(svcName string, iq *Instrumenter, sidecar *v1.Container) {
	sidecar.Env = append(sidecar.Env,
		v1.EnvVar{Name: "PROMETHEUS_SERVICE_NAME", Value: svcName},
		v1.EnvVar{Name: "PROMETHEUS_PORT", Value: strconv.Itoa(iq.Spec.Prometheus.Port)},
		// TODO: extra properties such as METRICS_REPORT_TARGET and METRICS_REPORT_PEER
	)
}

func findByName(containers []v1.Container) (*v1.Container, bool) {
	for c := range containers {
		if containers[c].Name == instrumenterName {
			return &containers[c], true
		}
	}
	return nil, false
}

// labelInstrumented annotates a pod as already being instrumented
func labelInstrumented(instrumenterName string, dst *v1.Pod) {
	if dst.Labels == nil {
		dst.Labels = map[string]string{}
	}
	dst.Labels[InstrumentedLabel] = instrumenterName
}

func unlabelInstrumented(dst *v1.Pod) {
	if len(dst.Labels) == 0 {
		return
	}
	delete(dst.Labels, InstrumentedLabel)
}