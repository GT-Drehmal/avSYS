package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// LabelOrg marks namespaces (and everything in them) that belong to an avSYS organization.
	LabelOrg = "avsys.dev/org"
	// LabelServer is set on every object owned by a MinecraftServer.
	LabelServer = "avsys.dev/server"
	// AnnotationAsleep is set on a MinecraftServer that was scaled down for being idle.
	// Removing it wakes the server.
	AnnotationAsleep = "avsys.dev/asleep"
	// AnnotationNode records the node chosen for a server when it was first
	// placed. Its volume lives there, so it keeps running there.
	AnnotationNode = "avsys.dev/node"
	// LabelNodeScore is set on nodes to their relative single-core speed.
	// New servers go to the highest-scoring node that has room.
	LabelNodeScore = "avsys.dev/score"
	// LabelNodeHosts set to "false" on a node keeps game servers off it.
	LabelNodeHosts = "avsys.dev/game-servers"
	// AnnotationSavingPaused is set while the panel has told a running game
	// to hold its saves for a snapshot. A panel that starts and finds it set
	// was interrupted before it could switch saving back on, and does so.
	AnnotationSavingPaused = "avsys.dev/saving-paused"
	// AnnotationRestoring is set, to the snapshot's ID, while a restore is
	// putting a server's files back, and removed when it has finished. One
	// that is still there with no restore running was cut short, and the
	// files may be a mix of two states: the game is not started on them.
	AnnotationRestoring = "avsys.dev/restoring"
	// AnnotationCreating is set next to AnnotationRestoring on a server that
	// is being made from a snapshot, and removed with it. One that still has
	// it with nothing running never received all of its files. There is
	// nothing in it worth keeping: it is deleted and made again.
	AnnotationCreating = "avsys.dev/creating"
	// AnnotationRestart is copied to the pod template; changing it rolls the pod.
	AnnotationRestart = "avsys.dev/restarted-at"
)

type DesiredState string

const (
	StateRunning DesiredState = "Running"
	StateStopped DesiredState = "Stopped"
)

type Loader string

const (
	LoaderVanilla  Loader = "vanilla"
	LoaderPaper    Loader = "paper"
	LoaderPurpur   Loader = "purpur"
	LoaderFolia    Loader = "folia"
	LoaderFabric   Loader = "fabric"
	LoaderQuilt    Loader = "quilt"
	LoaderForge    Loader = "forge"
	LoaderNeoForge Loader = "neoforge"
)

// ContentSource is where a mod, modpack or resource pack comes from.
type ContentSource string

const (
	SourceModrinth   ContentSource = "modrinth"
	SourceCurseForge ContentSource = "curseforge"
	SourceURL        ContentSource = "url"
	// SourceUpload is a file uploaded through the panel and kept on the
	// server's own volume; Project is its file name.
	SourceUpload ContentSource = "upload"
)

type ContentKind string

const (
	KindMod      ContentKind = "mod"
	KindPlugin   ContentKind = "plugin"
	KindDatapack ContentKind = "datapack"
)

// ContentRef is one installed mod, plugin or datapack.
type ContentRef struct {
	// +kubebuilder:validation:Enum=modrinth;curseforge;url;upload
	Source ContentSource `json:"source"`
	// +kubebuilder:validation:Enum=mod;plugin;datapack
	// +kubebuilder:default=mod
	Kind ContentKind `json:"kind,omitempty"`
	// Project is the Modrinth/CurseForge slug or ID, the download URL for
	// source "url", or the file name for source "upload".
	Project string `json:"project"`
	// Version pins a Modrinth version ID or CurseForge file ID. The panel
	// fills it in when an entry is added, so restarts do not change what is
	// installed. Empty (only possible for resources created outside the
	// panel) tracks the newest release compatible with the server.
	Version string `json:"version,omitempty"`
	// Java is the Java release the pinned file was compiled for, as read from
	// it by the panel. The server runs on at least this.
	Java int32 `json:"java,omitempty"`
	// Title and VersionName are display metadata recorded when the entry was added.
	Title       string `json:"title,omitempty"`
	VersionName string `json:"versionName,omitempty"`
	IconURL     string `json:"iconUrl,omitempty"`
}

// ModpackSpec installs a whole modpack. The pack decides the loader and game version.
type ModpackSpec struct {
	// +kubebuilder:validation:Enum=modrinth;curseforge
	Source  ContentSource `json:"source"`
	Project string        `json:"project"`
	// Version pins a Modrinth version ID or CurseForge file ID.
	Version string `json:"version,omitempty"`
	// GameVersion and Loader are what the pinned pack version is built for,
	// recorded by the panel. They decide the Java the server runs on and
	// which extra mods fit.
	GameVersion string `json:"gameVersion,omitempty"`
	Loader      string `json:"loader,omitempty"`
	Title       string `json:"title,omitempty"`
	VersionName string `json:"versionName,omitempty"`
	IconURL     string `json:"iconUrl,omitempty"`
}

// ResourcePackSpec is the server resource pack offered to joining players.
type ResourcePackSpec struct {
	URL string `json:"url"`
	// SHA1 of the pack. The panel fills this in when it can fetch the URL.
	SHA1    string `json:"sha1,omitempty"`
	Enforce bool   `json:"enforce,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}

type ExposeType string

const (
	// ExposeRouter shares the avSYS router's address; players are routed by hostname.
	ExposeRouter ExposeType = "Router"
	// ExposeLoadBalancer gives the server its own LoadBalancer Service.
	ExposeLoadBalancer ExposeType = "LoadBalancer"
	ExposeNodePort     ExposeType = "NodePort"
	// ExposeNone keeps the server reachable only inside the cluster.
	ExposeNone ExposeType = "None"
)

type ExposeSpec struct {
	// +kubebuilder:validation:Enum=Router;LoadBalancer;NodePort;None
	// +kubebuilder:default=Router
	Type ExposeType `json:"type,omitempty"`
	// Hostname players connect to, e.g. play.example.org. Required for Router.
	Hostname string `json:"hostname,omitempty"`
	// LoadBalancerIP requests a specific address (MetalLB, kube-vip, ...).
	LoadBalancerIP string `json:"loadBalancerIP,omitempty"`
	// NodePort requests a specific port for type NodePort.
	NodePort int32 `json:"nodePort,omitempty"`
	// ServiceAnnotations are added to the player-facing Service.
	ServiceAnnotations map[string]string `json:"serviceAnnotations,omitempty"`
}

// SleepSpec scales the server to zero while nobody is online. Waking on connect
// needs the router, so it only applies to expose type Router.
type SleepSpec struct {
	Enabled bool `json:"enabled,omitempty"`
	// +kubebuilder:default=15
	// +kubebuilder:validation:Minimum=1
	IdleMinutes int32 `json:"idleMinutes,omitempty"`
}

// BackupSpec says where a server departs from its organization's scheduled
// snapshots. Whatever is left out follows the organization.
type BackupSpec struct {
	// Enabled says whether the server gets scheduled snapshots at all.
	Enabled *bool `json:"enabled,omitempty"`
	// Minute of the day, in UTC, at which its snapshot is taken.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1439
	Minute *int32 `json:"minute,omitempty"`
	// Keep is how many of its scheduled snapshots are kept.
	Keep *BackupKeep `json:"keep,omitempty"`
}

// BackupKeep is how many scheduled snapshots to keep: the newest of each of
// the last so many days, weeks, months and years. The limits are those of an
// organization's schedule.
type BackupKeep struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=90
	Daily int32 `json:"daily"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=104
	Weekly int32 `json:"weekly"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=120
	Monthly int32 `json:"monthly"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=20
	Yearly int32 `json:"yearly"`
}

type StorageSpec struct {
	// +kubebuilder:default="10Gi"
	Size             resource.Quantity `json:"size,omitempty"`
	StorageClassName *string           `json:"storageClassName,omitempty"`
}

type ResourcesSpec struct {
	// Memory is the container memory limit. The JVM heap is sized below it,
	// leaving room for the memory Java uses outside the heap.
	// +kubebuilder:default="4Gi"
	Memory resource.Quantity `json:"memory,omitempty"`
	// CPU is an optional CPU limit.
	CPU *resource.Quantity `json:"cpu,omitempty"`
}

type MinecraftServerSpec struct {
	DisplayName string `json:"displayName,omitempty"`

	// +kubebuilder:validation:Enum=Running;Stopped
	// +kubebuilder:default=Running
	State DesiredState `json:"state,omitempty"`

	// EULA must be true: https://aka.ms/MinecraftEULA
	EULA bool `json:"eula"`

	// Version is the Minecraft version, or LATEST. Ignored when a modpack is set.
	// +kubebuilder:default=LATEST
	Version string `json:"version,omitempty"`
	// +kubebuilder:validation:Enum=vanilla;paper;purpur;folia;fabric;quilt;forge;neoforge
	// +kubebuilder:default=vanilla
	Loader        Loader `json:"loader,omitempty"`
	LoaderVersion string `json:"loaderVersion,omitempty"`
	// GameJava is the Java the game version ships with, where the version's
	// name does not say: a snapshot named by the week it came out in needs
	// what the release it led to needs. The panel fills it in when the
	// version is chosen. It is 0 where the name says, and where it is not
	// known.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=99
	GameJava int32 `json:"gameJava,omitempty"`

	Modpack      *ModpackSpec      `json:"modpack,omitempty"`
	Mods         []ContentRef      `json:"mods,omitempty"`
	ResourcePack *ResourcePackSpec `json:"resourcePack,omitempty"`

	// Properties are server.properties entries managed by avSYS. Keys not listed
	// here are left as they are in the file, so hand edits survive.
	Properties map[string]string `json:"properties,omitempty"`
	Ops        []string          `json:"ops,omitempty"`
	Whitelist  []string          `json:"whitelist,omitempty"`

	Resources ResourcesSpec `json:"resources,omitempty"`
	Storage   StorageSpec   `json:"storage,omitempty"`
	Expose    ExposeSpec    `json:"expose,omitempty"`
	Sleep     SleepSpec     `json:"sleep,omitempty"`
	// Backups is the server's own schedule for snapshots, where it has one.
	Backups *BackupSpec `json:"backups,omitempty"`

	// Image overrides the server image. By default a tag of itzg/minecraft-server
	// with the right Java for the game version is chosen.
	Image string `json:"image,omitempty"`
	// Env is passed to the server container after everything avSYS sets, for
	// itzg/minecraft-server options that have no field here.
	Env []corev1.EnvVar `json:"env,omitempty"`
	// NodeSelector constrains which nodes the server may run on.
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
}

type Phase string

const (
	PhaseStopped  Phase = "Stopped"
	PhaseStopping Phase = "Stopping"
	PhaseStarting Phase = "Starting"
	PhaseRunning  Phase = "Running"
	PhaseSleeping Phase = "Sleeping"
	PhaseError    Phase = "Error"
)

type PlayersStatus struct {
	Online int32    `json:"online"`
	Max    int32    `json:"max"`
	Names  []string `json:"names,omitempty"`
}

// SnapshotStatus is where a server stands with its snapshots.
type SnapshotStatus struct {
	// LastTime and LastID are the newest snapshot the panel took.
	LastTime *metav1.Time `json:"lastTime,omitempty"`
	LastID   string       `json:"lastID,omitempty"`
	// LastRun is when the scheduled snapshot last ran to completion, whether
	// it took a snapshot or found nothing changed since the one before.
	LastRun *metav1.Time `json:"lastRun,omitempty"`
	// Error says why the most recent snapshot or restore failed. It is
	// cleared by the next one that succeeds.
	Error string `json:"error,omitempty"`
}

type MinecraftServerStatus struct {
	Phase   Phase  `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
	// Address is what players type into the multiplayer screen.
	Address string `json:"address,omitempty"`
	// Version is the game version the running server reports.
	Version string         `json:"version,omitempty"`
	Players *PlayersStatus `json:"players,omitempty"`
	// Node is the cluster node the server runs on.
	Node string `json:"node,omitempty"`
	// LastActive is the last time a player was seen online, or the server became ready.
	LastActive *metav1.Time `json:"lastActive,omitempty"`
	Restarts   int32        `json:"restarts,omitempty"`
	// PendingRestart is true when the running server was started from an older
	// spec; the changes apply on the next restart.
	PendingRestart bool `json:"pendingRestart,omitempty"`
	// Snapshots is written by the panel after each snapshot or restore.
	Snapshots          *SnapshotStatus `json:"snapshots,omitempty"`
	ObservedGeneration int64           `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mcs
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Loader",type=string,JSONPath=`.spec.loader`
// +kubebuilder:printcolumn:name="Players",type=integer,JSONPath=`.status.players.online`
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.status.address`
type MinecraftServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MinecraftServerSpec   `json:"spec,omitempty"`
	Status MinecraftServerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MinecraftServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MinecraftServer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MinecraftServer{}, &MinecraftServerList{})
}
