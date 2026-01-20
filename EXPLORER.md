# **Project Skyhook Explorer: Strategic Viability Report for Standalone Open Source Release**

Date: January 19, 2026  
Prepared For: Project Skyhook Executive Leadership  
Prepared By: Principal Cloud Native Architect & Product Strategy Consultant

## **Executive Summary: The Strategic Imperative for a New Standard**

The following comprehensive feasibility study and strategic analysis serves as a definitive **GO** recommendation for the decoupling and release of Project Skyhook’s internal topology visualization technology as a standalone Open Source Software (OSS) tool. This recommendation is not merely a technical proposal; it is a strategic response to a distinct and lucrative anomaly in the current Kubernetes ecosystem: a "market vacuum" created by the convergence of legacy tool deprecation and the aggressive monetization of formerly open utilities.

As of early 2026, the Kubernetes visualization landscape has undergone significant consolidation:

- **The official Kubernetes Dashboard has been archived**, with Headlamp recommended as its replacement
- **Weave Scope is dead** following Weaveworks' shutdown, leaving a topology visualization vacuum
- **Freelens** has emerged to serve displaced Lens IDE users, but focuses on resource browsing, not topology
- **Enterprise platforms** (Komodor, Cast AI) offer advanced visualization behind steep paywalls

This creates a clear gap: **Headlamp and Freelens are resource browsers** (lists, details, YAML). **Topology visualization—understanding how resources relate—remains unsolved** in the open-source ecosystem.

Project Skyhook Explorer (PSE) is uniquely positioned to become the de facto standard for Kubernetes topology visualization—complementing Headlamp/Freelens rather than competing with them. PSE will be released as both a **fully standalone OSS tool** (direct K8s API access, local-first) and **integrated within the Skyhook platform** (connector-based, multi-cluster, enterprise features).

Success is contingent upon strict adherence to three strategic pillars: **uncompromising neutrality** (local execution without SaaS data tethering), **superior architectural rigor** (structured DAG layouts over chaotic force-directed graphs), and **Blue Ocean feature expansion** (integrating Network Policy visualization, cost attribution, and an events tray).

The detailed report that follows breaks down the competitive forensics, the necessary architectural extraction roadmap, and the go-to-market strategy required to transform Skyhook Explorer from an internal utility into a cornerstone of the Cloud Native Computing Foundation (CNCF) landscape.

## **Phase 1: Competitive Landscape & Gap Analysis**

To understand the viability of Skyhook Explorer, one must first perform a forensic analysis of the current market leaders and laggards. The analysis reveals a landscape defined not by innovation, but by abandonment and alienation. We have categorized the market into three distinct sectors: the "Dead" giants, the "Paywalled" incumbents, and the "Niche" alternatives.

### **1.1 The "Dead" Competitor: The Ghost of Weave Scope**

For nearly half a decade, **Weave Scope** served as the undisputed gold standard for Kubernetes visualization. Its ability to provide a real-time, zero-configuration map of containers, processes, and hosts set user expectations for what a topology tool should be. However, our deep investigation confirms that Weave Scope is functionally dead, leaving a massive functional gap in the open-source ecosystem.

The GitHub repository for Weave Scope now bears a stark warning: "PLEASE NOTE, THIS PROJECT IS NO LONGER MAINTAINED".1 This deprecation was not merely a pause in development but a terminal event precipitated by the business difficulties of its parent company, Weaveworks, which ceased operations in early 2024\.2 While forks exist, the momentum has vanished. The project has moved into a "zombie" state—still downloaded by users desperate for visualization, but riddled with unpatched vulnerabilities and lacking support for modern Kubernetes APIs (v1.28+).

The "ghost" of Weave Scope looms large because it established a specific user requirement that modern tools fail to meet: **Zero-Config Discovery**. Users loved Scope because it did not require manual tagging or complex setups. It inspected the kernel and network interfaces to draw lines between services automatically.1 Current dashboards often present resources in a flat, spreadsheet-like list, failing to visualize the *relationships* (edges) between them. Skyhook Explorer’s primary mandate is to replicate this "magical" discovery capability using modern, sustainable architecture, leveraging Service/Ingress relationships and OwnerReferences 3 rather than the heavy kernel-level introspection that made Scope difficult to maintain.

### **1.2 The "Paywalled" Competitor: The Alienation of Lens Users**

**Lens IDE**, initially developed by Kontena and later acquired by Mirantis, is the dominant desktop client for Kubernetes management.4 For years, it operated under a permissive model that built a massive user base. However, recent strategic shifts toward monetization have created a significant fracture in its community—a fracture Skyhook is perfectly positioned to exploit.

The core grievance among the community revolves around the "Resource Map" visualization and advanced security features, which were effectively moved behind the "Lens Pro" subscription paywall.5 While a "Lens Personal" tier exists, it is restricted to individuals or startups with less than $10 million in revenue 5, and it requires a mandatory cloud login (Lens ID), which many privacy-conscious engineers find unacceptable.

The sentiment on community forums such as Reddit and Hacker News is unambiguous. Threads discussing Lens frequently devolve into complaints about "bloat," "forced logins," and the removal of features that were once free.7 Users describe the shift as a "bait-and-switch," creating a deep reservoir of resentment.8 Even the open-source core, "OpenLens," often ships without the visualization extensions that made the product compelling, forcing users to hunt for third-party plugins that may or may not be maintained.

This alienation presents a tactical opening. By explicitly marketing Skyhook Explorer as "Free Forever OSS" and using a permissive license (Apache 2.0 or MIT), we can attract the "disenfranchised Lens user." The marketing narrative should subtly but clearly contrast Skyhook’s "local-first, no-login" philosophy against the "cloud-tethered, paywalled" model of Lens.

### **1.3 The New Landscape: Dashboard Archived, Headlamp Recommended**

A critical development in late 2025 reshapes the competitive landscape: **the official Kubernetes Dashboard has been archived**. The repository now states the project is "no longer maintained due to lack of active maintainers" and explicitly recommends **Headlamp** as the replacement. Headlamp has since moved under `kubernetes-sigs/headlamp` and is now part of Kubernetes SIG-UI.

This creates a clarified market structure:
- **Headlamp** is the new default for resource browsing, lists, and details
- **Freelens** (4.4k stars, actively maintained) captures displaced Lens IDE users
- **Neither provides topology-first visualization**

The opportunity is not "Lens users are stranded" — it's that **topology and dependency understanding remains an unsolved problem**. Dashboards and IDEs are resource browsers; they show lists and YAML. They don't show relationships.

### **1.4 Active Competitors: Limitations & Niches**

Beyond the major players, several smaller tools exist, but each suffers from critical limitations that prevent them from achieving mass adoption.

#### **KubeView: The Structural Weakness**

**KubeView** is often cited as the lightweight alternative to Weave Scope.9 While it is a functional visualizer, it suffers from a fatal flaw in its visualization engine: the reliance on **force-directed layouts**.

Force-directed algorithms simulate physics, where nodes repel each other and edges act as springs.10 While this creates a dynamic, "bouncy" animation that looks impressive in a demo of 10 pods, it fails catastrophically at scale. In clusters with 100+ nodes, force-directed graphs become "hairballs"—unreadable tangles of edges that constantly shift and jitter.11 They lack semantic structure; a database might float above a frontend simply because of the physics simulation, confusing the logical architecture.

Skyhook must differentiate itself by rejecting force-directed physics for its default view. Instead, we must utilize a **Hierarchical DAG (Directed Acyclic Graph)** layout. This layout enforces a logical flow—typically left-to-right or top-down (Ingress → Service → Pod → PVC)—that respects the architectural reality of microservices.13 This structured approach is essential for "Day 2" operations where clarity trumps animation.

#### **Freelens: IDE Competitor, Not Topology Tool**

**Freelens** is an actively maintained fork of OpenLens with 4.4k GitHub stars and regular releases. It successfully captures users displaced by Lens's licensing changes, offering a full Kubernetes IDE experience.

However, Freelens is fundamentally an **IDE/resource browser**, not a topology visualizer. It provides lists, YAML editing, and resource details—the same job as Headlamp, just in a desktop form factor. It does not provide structured dependency visualization.

**Competitive Assessment:** Freelens is not a direct competitor. Users wanting "Lens without the licensing" use Freelens. Users wanting "see how my resources relate" need Explorer. Different jobs, different tools.

#### **Headlamp: Complement, Not Compete**

As noted in Section 1.3, **Headlamp** is now the officially recommended replacement for the Kubernetes Dashboard and has strong institutional backing (Microsoft/Kinvolk, Kubernetes SIG-UI). It offers an extensible, plugin-based architecture.

We considered whether Skyhook Explorer should be a Headlamp plugin rather than a standalone tool. The analysis concludes: **both, but standalone first**.

* **Why not plugin-only:** A plugin-only strategy tethers our reach to Headlamp's adoption curve. Users who prefer Freelens, K9s, or pure CLI workflows would be excluded.
* **Why plugin later:** Headlamp's growing adoption makes it a valuable distribution channel. A Headlamp plugin in V2 expands reach without limiting it.
* **Positioning:** Headlamp is a resource browser (lists, details, YAML). Explorer is a topology visualizer (relationships, dependencies). They serve different jobs and complement each other.

**Strategic Decision:** Release Explorer as a **standalone binary** first (OSS + Skyhook integrated). Subsequently release a Headlamp plugin wrapper to embed Explorer's topology view within Headlamp.

#### **Komodor: The Enterprise Ceiling**

**Komodor** is a powerful platform focused on troubleshooting and change intelligence.4 It offers excellent visualization and timelines, but it is fundamentally an enterprise SaaS product.

* **Pricing Friction:** Komodor's pricing model is often per-node, and recent changes have eliminated generous free tiers, creating a barrier for entry for individual developers and small teams.8  
* **Data Sovereignty:** As a SaaS platform, Komodor requires cluster metadata to be sent to their cloud for processing.16 For organizations in regulated industries (Finance, Healthcare, Defense), this "call home" requirement is a non-starter. Skyhook, by running entirely locally or in-cluster without exfiltrating data, wins immediately on data sovereignty and privacy.

### **1.5 Comparative Feature Matrix**

The following table summarizes the competitive landscape, highlighting the specific feature gaps Skyhook Explorer is designed to fill.

| Feature | Skyhook Explorer | Weave Scope | Headlamp | Freelens | KubeView | Komodor | KHI |
| :---- | :---- | :---- | :---- | :---- | :---- | :---- | :---- |
| **Status** | **Proposed** | Deprecated | Active (K8s SIG) | Active | Active | Active | Active (GKE) |
| **Primary Job** | **Topology/Relationships** | Topology | Resource Browser | IDE | Topology | Troubleshooting | Timeline |
| **License** | **Open Source** | OSS (Dead) | Apache 2.0 | MIT | MIT | Proprietary | Apache 2.0 |
| **Deployment** | **Local + In-Cluster** | DaemonSet | Desktop + In-Cluster | Desktop | In-Cluster | SaaS Agent | GKE Only |
| **Visualization** | **Structured DAG** | Force-Directed | Lists/Details | Lists/Details | Force-Directed | Service Map | Timeline |
| **Data Privacy** | **100% Local** | Local | Local | Local | Local | Cloud Required | GCP Logging |
| **Network Policy** | **Visual Overlay** | No | No | No | No | Enterprise | No |
| **Events Timeline** | **Events Tray (MVP)** | No | Basic | No | No | Full Timeline | Full Timeline |
| **Cost Insights** | **OpenCost Integration** | No | No | No | No | Paid | No |
| **Multi-Cluster** | **OSS: context switch / Skyhook: native** | No | Yes | Yes | No | Yes | No |

### **1.6 Gap Analysis Conclusion**

The market analysis reveals a clear "Blue Ocean." The competitive landscape has bifurcated:

- **Resource Browsers** (Headlamp, Freelens): Excellent for lists, details, YAML editing — but not topology-first
- **Topology Tools** (KubeView, Weave Scope): Graph-based but either dead (Scope) or architecturally flawed (force-directed layouts)
- **Enterprise Platforms** (Komodor): Full-featured but paywalled and SaaS-dependent

There is currently no tool that is simultaneously **Topology-First** (relationships, not lists), **Structured** (DAG-based, not force-directed), **Free/Open**, and **Private** (local-first). Skyhook Explorer is positioned to fill this exact gap, complementing Headlamp/Freelens rather than competing with them directly.

## **Phase 2: Technical Feasibility & Architecture**

Releasing Skyhook's internal topology tool as a public OSS project requires a clear architectural strategy that serves two deployment models:

1. **OSS Standalone**: Direct Kubernetes API access via Informers, local-first, no Skyhook dependency
2. **Skyhook Integrated**: Embedded within the Skyhook platform, leveraging the connector architecture for multi-cluster access without direct cluster credentials

**Critical Design Principle:** The OSS version must be fully functional standalone. Skyhook integration is an additional deployment option, not a constraint on the core design. This ensures:
- OSS users get a complete, valuable tool without vendor lock-in
- Skyhook customers get the same visualization with platform benefits (auth, multi-cluster, audit)
- Shared UI components reduce maintenance burden

### **2.1 Dual-Track Architecture**

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        Skyhook Explorer Architecture                         │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────────────────────┐  ┌─────────────────────────────┐  │
│  │       OSS Standalone Mode           │  │   Skyhook Integrated Mode   │  │
│  ├─────────────────────────────────────┤  ├─────────────────────────────┤  │
│  │                                     │  │                             │  │
│  │  Browser ──▶ Explorer Backend       │  │  Skyhook UI ──▶ Explorer UI │  │
│  │                   │                 │  │  (embedded)        │        │  │
│  │                   ▼                 │  │                    ▼        │  │
│  │           SharedInformers           │  │           koala-backend     │  │
│  │                   │                 │  │                    │        │  │
│  │                   ▼                 │  │                    ▼        │  │
│  │         Kubernetes API Server       │  │          CAC ──▶ Connector  │  │
│  │         (direct, via kubeconfig)    │  │                    │        │  │
│  │                                     │  │                    ▼        │  │
│  │                                     │  │            K8s API Server   │  │
│  └─────────────────────────────────────┘  └─────────────────────────────┘  │
│                                                                             │
│  Shared: React UI components, DAG layout engine, Network Policy parser     │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### **2.2 Dependency Extraction Strategy**

For the OSS standalone mode, the core technical challenge is ensuring the visualization logic works independently with direct Kubernetes API access.

Key Architectural Decision: Direct API vs. Intermediate Cache  
A naive implementation might query the Kubernetes API (e.g., kubectl get pods) every time the graph needs to be rendered. This is known as the "Direct API" approach. However, for a visualization tool that might auto-refresh every few seconds, this approach is non-scalable. It risks overwhelming the Kubernetes API server—a phenomenon known as the "thundering herd"—especially during high-traffic incidents or in large clusters.  
Therefore, the recommended architecture for Skyhook Explorer is the **Intermediate Cache** model. We must implement a lightweight, in-memory cache within the Skyhook Explorer binary itself. This cache will maintain the current state of the cluster graph (nodes and edges) and serve frontend requests instantly without hitting the K8s API for every UI repaint.

### **2.2 Data Ingestion: The Supremacy of Informers**

To populate this local cache, we have two primary data collection strategies: Polling and Informers.

**Polling:** This involves periodically running list operations (e.g., every 10 seconds). This is the "easy" way, but it is fundamentally flawed for a visualizer. It introduces significant latency—users might delete a pod and still see it on the map for 10 seconds. Furthermore, repeated list operations on large clusters are expensive for the API server (O(n) complexity).

**Informers (Client-Go):** The superior strategy, and the one we must adopt, is the use of Kubernetes **SharedInformers** from the client-go library.17

* **Mechanism:** Informers leverage the Kubernetes Watch API. Instead of asking "what is the state now?", an Informer establishes a persistent connection and asks, "tell me when something changes."  
* **The Reflector Pattern:** Inside client-go, a component called the *Reflector* watches for changes. When a Pod changes state (e.g., from Running to CrashLoopBackOff), the API server pushes a delta event to the Informer. The Informer then updates the local in-memory cache (the Indexer).  
* **Event-Driven UI:** This architecture allows Skyhook Explorer to push updates to the frontend via WebSockets or Server-Sent Events (SSE).9 The result is a UI where a pod turns red the *millisecond* it crashes, creating a "Time to Wow" that polling tools cannot match.  
* **Optimization:** We must utilize the ResourceVersion capabilities of the ListWatch pattern to ensure that, upon restart, the tool only syncs changes that occurred since the last known state, minimizing bandwidth usage.18

### **2.3 Visualization Engine: The Case for a Structured DAG**

The choice of the frontend visualization library is perhaps the single most critical User Experience (UX) decision. It determines whether the tool feels "professional" or "toy-like."

The Trap of Force-Directed Graphs:  
Many open-source tools, including KubeView, utilize libraries like react-force-graph or D3's force simulation.9 These libraries are attractive because they are easy to implement—you throw nodes and edges into the engine, and physics takes over. However, for representing software architecture, they are fundamentally flawed.

* **Non-Determinism:** Every time you load the page, the graph looks different. This frustrates users who build a spatial memory of their system ("The database is always on the right").  
* **The "Wobble":** In large clusters, the simulation never quite settles. Nodes constantly jitter, making it difficult to click or inspect them.  
* **Lack of Semantic Layers:** Force-directed layouts place nodes based on connectivity, not hierarchy. A database might appear "above" a frontend service, which is architecturally counter-intuitive.

The Solution: Cytoscape.js with Hierarchical Layouts  
We strongly recommend using Cytoscape.js 20 coupled with a hierarchical layout engine such as dagre (for smaller graphs) or klay/elk (for larger, complex graphs).

* **Structured Layers:** This approach allows us to enforce specific architectural layers:  
  * *Layer 1 (Top):* Ingress Controllers / Load Balancers  
  * *Layer 2:* Services (The logical abstraction)  
  * *Layer 3:* Workloads (Deployments, StatefulSets, DaemonSets)  
  * *Layer 4:* Pods (The actual compute units)  
  * *Layer 5 (Bottom):* External Resources / Persistent Volumes  
* **Performance:** Cytoscape.js offers a Canvas renderer and an optional WebGL renderer. Benchmarks indicate that WebGL rendering can handle 1,000+ nodes at 60 FPS, whereas DOM-based renderers (like those used in some React libraries) often choke at 500 nodes.22 This performance headroom is essential for enterprise adoption, where a single cluster might run thousands of pods.

### **2.4 Installation & Distribution: "Time to Wow"**

In the open-source world, friction is the enemy of adoption. Users will not spend an hour configuring a tool just to see if it works. Skyhook Explorer must achieve a "Time to Wow" of under 60 seconds.

1\. The kubectl Plugin (Primary Distribution):  
The gold standard for CLI extension is Krew.23 By packaging Skyhook as a Krew plugin, we allow users to install it with a single command:  
kubectl krew install skyhook  
Once installed, the user simply types kubectl skyhook. The binary spins up locally, uses the user's existing kubeconfig credentials (respecting all RBAC rules), and opens the dashboard in their default browser. This requires zero cluster changes—no YAML to apply, no permissions to grant, no security review required. This is the lowest barrier to entry and the most viral distribution method.  
2\. Docker Container (Alternative):  
For users who prefer containerized workflows, we should provide a lightweight Docker image:  
docker run \-v \~/.kube:/root/.kube \-p 8080:8080 skyhook/explorer  
This is particularly useful for users on restricted machines where installing binaries is difficult, but Docker is available.  
3\. Helm Chart (Secondary):  
While the local binary is best for individual adoption, teams will eventually want a persistent dashboard running inside the cluster. A Helm chart allows DevOps engineers to deploy Skyhook as a Service.25 However, we must ensure the Docker image is optimized (\<50MB) to ensure fast pull times and minimal resource footprint on the cluster.

## **Phase 3: "Blue Ocean" Feature Expansion**

To ensure Skyhook Explorer isn't perceived as just "another KubeView clone," we must integrate high-value features that competitors typically ignore or gate behind expensive paywalls. These "Blue Ocean" features target specific pain points—Security and Cost—that transform the tool from a "nice-to-have" visualizer into a "must-have" operational auditing platform.

### **3.1 Network Policy Overlay (The "Cilium" Gap)**

Kubernetes Network Policies are notoriously difficult to visualize. They are defined in complex YAML files that use label selectors to allow or deny traffic. Effectively, they act as an "invisible firewall"—engineers often don't know a policy exists until their application breaks.

**The Feature:** Skyhook Explorer will parse NetworkPolicy objects in real-time and overlay their effects directly onto the topology graph.

**Implementation Logic:**

1. **Ingestion:** The Informer watches networking.k8s.io/v1/NetworkPolicy resources.  
2. **Selector Matching:** The backend logic parses the podSelector (target) and the ingress/egress rules (sources/destinations).  
3. **Graph Decoration:**  
   * **Allowed Paths:** If a policy explicitly allows traffic between Pod A and Pod B, we draw a solid **Green Line**.  
   * **Blocked Paths:** If Pod A attempts to talk to Pod B, but no policy allows it (or a deny-all is in place), we render a **Red Dotted Line**. This can be inferred by checking the allowed status of the edge against the active policies.  
   * **Isolation Badges:** Pods that are "isolated" (selected by a policy but with no allow rules) receive a visual "Lock" icon.

**Value Proposition:** This turns Skyhook into a security auditing tool. "Why can't Service A talk to Service B?" becomes visually obvious. While standalone tools like the Cilium Editor exist 26, they are often disconnected from the live cluster state. Integrating this *into the main topology* is a massive differentiator that appeals to security-conscious enterprises.

### **3.2 Cost Attribution (The "FinOps" Angle)**

Engineers often deploy workloads without understanding the cost implications. "FinOps" is usually the domain of managers looking at spreadsheets, disconnected from the developers managing the pods.

**Integration:** Skyhook can bridge this gap by querying the **OpenCost API** (if installed in the cluster).28 OpenCost is the open-source standard for Kubernetes cost monitoring.

* **Visual Badge:** We can append a small badge to each Namespace, Deployment, or Service node showing its estimated cost (e.g., "$12/day").  
* **Feasibility:** The OpenCost Allocation API is standardized and JSON-based. Skyhook does not need to calculate costs itself; it simply acts as a presentation layer for the OpenCost data.  
* **Impact:** This adds a "FinOps" dimension to the topology. A developer seeing a bright red "$500/day" badge on a testing pod is far more likely to downscale it than if that data were buried in a monthly finance report.

### **3.3 Events Visualization (Phased Approach)**

A common question during incidents is: "What changed right before the crash?" Most dashboards show Kubernetes Events (e.g., BackOff, FailedScheduling) as a dry, chronological text list.

**Important:** Full timeline visualization (à la Komodor) requires persistence, storage, and correlation complexity. To ship fast without scope creep, we adopt a phased approach:

#### **MVP: Events Tray (Low Complexity)**

A lightweight events panel integrated into the topology view:

* **Node-Scoped Events:** Click any resource in the topology → see recent events for that resource
* **Cluster Events Stream:** Collapsible tray showing recent cluster-wide events (last 100, no persistence)
* **Visual Indicators:** Resources with recent warning/error events get a badge on the topology node
* **No Persistence:** Events come from K8s API in real-time, not stored

This captures 80% of the "what just happened?" value with 20% of the complexity.

#### **V2: Full Timeline (Future Phase)**

The full "DVR-style" interactive timeline for post-MVP:

* **Visualization:** A density plot or heat map showing the volume of events over time. Spikes indicate bursts of activity.
* **Interaction:** Clicking a specific point on the timeline (e.g., "10:42 AM") filters the topology graph to highlight the nodes that generated events at that moment.
* **Persistence:** Requires backend storage for historical data (hours/days of retention).
* **Correlation:** Link events to deployments, Git commits, and config changes.

**Note:** Google's Kubernetes History Inspector (KHI) exists in this space for GKE users. Our V2 timeline should differentiate by being cloud-agnostic and integrated with the topology view rather than log-centric.

## **Phase 4: Marketing & Distribution Channels**

Even the most technically superior tool can fail without a robust Go-to-Market (GTM) strategy. The distribution channels must be selected to maximize developer awareness and minimize friction.

### **4.1 The "Product Hunt" Launch Strategy**

Developer tools have a strong track record on Product Hunt if positioned correctly.30 The key is to frame Skyhook not just as a "tool," but as a "solution to a missing workflow."

Positioning: "The Missing GUI for Kubernetes." The tagline should play on the nostalgia for Weave Scope: "The visualization you loved, modernized for 2026."  
Assets: A high-quality demo video is mandatory. It must clearly demonstrate the "Time to Wow"—specifically, the process of installing the plugin via Krew and seeing the live map in under 60 seconds.  
Timing: Product Hunt algorithm favors launches early in the week (Tuesday or Wednesday) at 12:01 AM PST to maximize the voting window.

### **4.2 CNCF Sandbox Submission**

Getting accepted into the **CNCF Sandbox** is a critical milestone for long-term viability.32 It serves as a powerful validator, signaling to enterprises that the project is not merely a "vendor toy" but a serious, neutral open-source initiative.

* **Neutrality:** CNCF acceptance alleviates "vendor lock-in" fears. Enterprises are far more likely to adopt a tool that is part of the CNCF landscape than one owned solely by "Skyhook Corp."  
* **Timing:** The application should be submitted *after* the project reaches approximately 1,000 GitHub stars. This demonstrates initial community traction and viability, which are prerequisites for Sandbox consideration.

### **4.3 Monetization: The "Open Core" Funnel Strategy**

While Skyhook Explorer is free OSS, it serves as a strategic acquisition channel for Skyhook’s commercial platform. To balance community goodwill with revenue generation, we recommend an **Open Core** model. This model relies on a "features funnel" where the OSS version covers 90% of individual use cases, but the Enterprise version captures the high-value organizational needs.34

**The Open Core Funnel Structure:**

1. **Top of Funnel (The OSS User):**  
   * *User Profile:* Individual developers, DevOps engineers, homelab users.  
   * *Capabilities:* Single-cluster visualization, real-time data, Network Policy overlays, local execution.  
   * *Goal:* Ubiquity and brand loyalty. This tier establishes Skyhook as the standard tool.  
2. **The "Trigger" Points (Transition to Paid):**  
   * **Multi-Cluster Aggregation:** The OSS tool connects to one cluster at a time (context switching). The Enterprise platform aggregates 10, 50, or 100 clusters into a "Single Pane of Glass." This is a pain point only for large organizations, making it a perfect upsell trigger.  
   * **Historical Retention:** "Show me the topology from last Tuesday at 2:00 AM." The OSS tool relies on live data and local memory. Storing weeks of topology history requires a persistent database and backend storage—value that enterprises are willing to pay for to support compliance and post-mortem analysis.  
   * **SSO & RBAC Integration:** Integration with enterprise identity providers (Okta, Active Directory) and granular Role-Based Access Control (e.g., "Interns can see the map but cannot view Secrets") is a classic enterprise feature.36  
3. **Bottom of Funnel (Enterprise Customer):**  
   * *Value:* Operational efficiency, compliance, security governance, and support SLAs.

By clearly delineating these tiers, Skyhook can aggressively distribute the free tool without cannibalizing its enterprise revenue. The OSS tool becomes the marketing engine, filling the top of the funnel with qualified users who are already trained on the interface.

## **5\. Strategic Roadmap & MVP Definition**

To achieve a successful launch in Q3 2026, the following phased execution roadmap is proposed.

**Phase 1: The "Lite" Extraction (Weeks 1-6)**

* **Goal:** Create standalone OSS foundation with direct K8s API access.
* **Key Tasks:**
  * Implement client-go SharedInformers for core resources (Pods, Deployments, Services, Ingress, ConfigMaps, Secrets, ReplicaSets)
  * Basic web UI with live resource display
  * CLI binary that opens localhost dashboard
* **Deliverable:** A working `skyhook-explorer` binary that shows live cluster resources.

**Phase 2: The "Structure" + Events Tray (Weeks 7-12)**

* **Goal:** Solve the "hairball" problem + capture "what happened?" value.
* **Key Tasks:**
  * Implement Cytoscape.js with DAG layout engine (dagre/elk)
  * Logical grouping by Namespace and Controller (Pods inside Deployments)
  * **Events Tray:** Node-scoped events panel + cluster events stream (no persistence)
  * Visual badges for resources with recent warnings/errors
* **Deliverable:** A structured, readable topology map with integrated events — already better than KubeView.

**Phase 3: Blue Ocean Features (Weeks 13-16)**

* **Goal:** Create market differentiation with security and cost visibility.
* **Key Tasks:**
  * Network Policy parser and visual overlay (green=allowed, red=blocked)
  * OpenCost API integration for cost badges
  * Skyhook integration layer (optional connector-based data source)
* **Deliverable:** The "killer features" that justify adoption over simpler alternatives.

**Phase 4: Launch Prep (Weeks 17-20)**

* **Goal:** Polish, distribution, and launch.
* **Key Tasks:**
  * Submit to Krew index (`kubectl krew install skyhook-explorer`)
  * Helm chart with GitHub Pages hosting
  * Docker image (<50MB)
  * Launch video demonstrating "Time to Wow" < 60 seconds
  * Documentation site
* **Deliverable:** Public OSS release.

**Phase 5: Post-Launch (V2 Features)**

* Full DVR-style events timeline with persistence
* RBAC visualization (who can access what)
* Headlamp plugin packaging
* CNCF Sandbox submission (target: after 1,000 GitHub stars)

## **Conclusion**

The market conditions for **Project Skyhook Explorer** are uniquely favorable. The convergence of three major events—the archival of the official Kubernetes Dashboard, the death of Weave Scope, and the bifurcation of the market into resource browsers (Headlamp, Freelens) vs. enterprise platforms (Komodor)—has created a clear gap: **topology visualization remains unsolved in open source**.

By combining the **zero-config discovery** that users loved in Scope, the **structured visualization** of a DAG layout, and the **Blue Ocean features** of Network Policy overlay and cost attribution, Skyhook Explorer can establish itself as the de facto topology standard—complementing Headlamp rather than competing with it.

The dual-track architecture (OSS standalone + Skyhook integrated) ensures maximum reach while maintaining strategic value for the platform. OSS users get a complete, valuable tool. Skyhook customers get the same visualization with platform benefits.

This project is not merely a tool release; it is a strategic maneuver to establish Skyhook as a leader in the open-source community, driving top-of-funnel adoption for the broader enterprise platform. We recommend immediate project commencement.

## **6\. Risk Assessment & Mitigation**

**Risk 1: Performance on Massive Clusters**

* **Issue:** On clusters with 5,000+ pods, client-side rendering (WebGL) or the client-go cache memory footprint might become unmanageable for a local binary.  
* **Mitigation:** Implement a "Safe Mode." If the tool detects \>500 nodes on startup, it should default to a "Namespace Filter" view rather than attempting to render the entire cluster map. This prevents browser crashes and ensures usability at scale.

**Risk 2: "Sherlocking" by Cloud Vendors**

* **Issue:** AWS (EKS Console) or Google (GKE Dashboard) might release a native free topology view, making third-party tools redundant.  
* **Mitigation:** Focus relentlessly on **Cross-Cloud** and **Local** capability. AWS's tool will never visualize GKE clusters or local Minikube instances well. Skyhook’s neutrality and ability to run anywhere (even air-gapped) is its primary defense against cloud provider consolidation.

**Risk 3: Headlamp/Freelens Add Topology Features**

* **Issue:** Headlamp or Freelens could add native topology visualization, reducing Explorer's differentiation.
* **Mitigation:** Ship fast and establish mindshare. First-mover advantage in topology is significant. Additionally, our DAG-based architecture and Blue Ocean features (Network Policy overlay, cost badges) create defensible differentiation beyond basic topology. Offer Headlamp plugin in V2 to turn potential competition into distribution channel.

**Risk 4: Maintenance Burnout**

* **Issue:** Successful OSS projects can drown maintainers in GitHub Issues and feature requests.  
* **Mitigation:** Clearly define the project scope in the README. "Skyhook Explorer is a read-only visualizer, not a cluster management dashboard." We must reject feature requests that turn it into a full management plane (like editing YAMLs or opening terminal shells) to keep the maintenance burden low and the code surface area minimal.

---

**End of Report.**

#### **Works cited**

1. weaveworks/scope: Monitoring, visualisation & management for Docker & Kubernetes \- GitHub, accessed January 19, 2026, [https://github.com/weaveworks/scope](https://github.com/weaveworks/scope)  
2. Weaveworks Shuts Down: Legacy and Next Steps \- Codefresh, accessed January 19, 2026, [https://codefresh.io/learn/weaveworks-shuts-down-legacy-and-next-steps/](https://codefresh.io/learn/weaveworks-shuts-down-legacy-and-next-steps/)  
3. Owners and Dependents \- Kubernetes, accessed January 19, 2026, [https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/](https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/)  
4. Kubernetes Lens 6: Basics, Quick Tutorial, and 3 Great Alternatives \- Komodor, accessed January 19, 2026, [https://komodor.com/learn/kubernetes-lens/](https://komodor.com/learn/kubernetes-lens/)  
5. Lens Subscription and Licensing FAQ \- Lens Documentation, accessed January 19, 2026, [https://docs.k8slens.dev/faq/subscription-and-licensing/](https://docs.k8slens.dev/faq/subscription-and-licensing/)  
6. Getting Started with Lens \- Lens Support & Troubleshooting \- Lens Forums, accessed January 19, 2026, [http://forums.k8slens.dev/t/getting-started-with-lens/104](http://forums.k8slens.dev/t/getting-started-with-lens/104)  
7. Kubernetes Dashboard being retired \- Reddit, accessed January 19, 2026, [https://www.reddit.com/r/kubernetes/comments/1q4szqy/kubernetes\_dashboard\_being\_retired/](https://www.reddit.com/r/kubernetes/comments/1q4szqy/kubernetes_dashboard_being_retired/)  
8. Komodor Just Pulled the Ultimate Bait-and-Switch Move by Killing Their Freemium Plan : r/kubernetes \- Reddit, accessed January 19, 2026, [https://www.reddit.com/r/kubernetes/comments/1ewsa82/komodor\_just\_pulled\_the\_ultimate\_baitandswitch/](https://www.reddit.com/r/kubernetes/comments/1ewsa82/komodor_just_pulled_the_ultimate_baitandswitch/)  
9. KubeView is a Kubernetes cluster visualization tool that provides a graphical representation of your cluster's resources and their relationships \- GitHub, accessed January 19, 2026, [https://github.com/benc-uk/kubeview](https://github.com/benc-uk/kubeview)  
10. Force-directed graph drawing \- Wikipedia, accessed January 19, 2026, [https://en.wikipedia.org/wiki/Force-directed\_graph\_drawing](https://en.wikipedia.org/wiki/Force-directed_graph_drawing)  
11. How to make a 10,000 node graph performant : r/reactjs \- Reddit, accessed January 19, 2026, [https://www.reddit.com/r/reactjs/comments/1epvcol/how\_to\_make\_a\_10000\_node\_graph\_performant/](https://www.reddit.com/r/reactjs/comments/1epvcol/how_to_make_a_10000_node_graph_performant/)  
12. Improving performance for extremely large datasets · Issue \#202 · vasturiano/react-force-graph \- GitHub, accessed January 19, 2026, [https://github.com/vasturiano/react-force-graph/issues/202](https://github.com/vasturiano/react-force-graph/issues/202)  
13. Transformation of Directed Acyclic Graphs into Kubernetes Deployments with Optimized Latency \- Diva-Portal.org, accessed January 19, 2026, [https://www.diva-portal.org/smash/get/diva2:1710657/FULLTEXT01.pdf](https://www.diva-portal.org/smash/get/diva2:1710657/FULLTEXT01.pdf)  
14. Headlamp Plugins, accessed January 19, 2026, [https://headlamp.dev/plugins/](https://headlamp.dev/plugins/)  
15. Headlamp, accessed January 19, 2026, [https://headlamp.dev/](https://headlamp.dev/)  
16. Komodor | Pricing and Plans, accessed January 19, 2026, [https://komodor.com/platform/pricing-and-plans/](https://komodor.com/platform/pricing-and-plans/)  
17. Mastering Kubernetes Informers: A Deep Dive \- Plural.sh, accessed January 19, 2026, [https://www.plural.sh/blog/manage-kubernetes-events-informers/](https://www.plural.sh/blog/manage-kubernetes-events-informers/)  
18. A Deep Dive Into Kubernetes Client-Go Informers | by Md Shamim | Level Up Coding, accessed January 19, 2026, [https://levelup.gitconnected.com/a-deep-dive-into-kubernetes-client-go-informers-012bb5362a38](https://levelup.gitconnected.com/a-deep-dive-into-kubernetes-client-go-informers-012bb5362a38)  
19. Kubernetes Informers are so easy... to misuse\! | Render Blog, accessed January 19, 2026, [https://render.com/blog/kubernetes-informers](https://render.com/blog/kubernetes-informers)  
20. What is the difference between D3.js and Cytoscape.js? \[closed\] \- Stack Overflow, accessed January 19, 2026, [https://stackoverflow.com/questions/16776005/what-is-the-difference-between-d3-js-and-cytoscape-js](https://stackoverflow.com/questions/16776005/what-is-the-difference-between-d3-js-and-cytoscape-js)  
21. Cytoscape.js, accessed January 19, 2026, [https://js.cytoscape.org/](https://js.cytoscape.org/)  
22. WebGL Renderer Preview \- Cytoscape.js, accessed January 19, 2026, [https://blog.js.cytoscape.org/2025/01/13/webgl-preview/](https://blog.js.cytoscape.org/2025/01/13/webgl-preview/)  
23. Extend kubectl with plugins \- Kubernetes, accessed January 19, 2026, [https://kubernetes.io/docs/tasks/extend-kubectl/kubectl-plugins/](https://kubernetes.io/docs/tasks/extend-kubectl/kubectl-plugins/)  
24. Using Custom Plugin Indexes \- Krew, accessed January 19, 2026, [https://krew.sigs.k8s.io/docs/user-guide/custom-indexes/](https://krew.sigs.k8s.io/docs/user-guide/custom-indexes/)  
25. Chart Releaser Action to Automate GitHub Page Charts \- Helm, accessed January 19, 2026, [https://helm.sh/docs/howto/chart\_releaser\_action/](https://helm.sh/docs/howto/chart_releaser_action/)  
26. artturik/network-policy-viewer: View your Kubernetes NetworkPolicy manifests as graph, online version available at https://artturik.github.io/network-policy-viewer \- GitHub, accessed January 19, 2026, [https://github.com/artturik/network-policy-viewer](https://github.com/artturik/network-policy-viewer)  
27. Cilium Network Policy Editor \- Network Policy Editor for Kubernetes, accessed January 19, 2026, [https://editor.networkpolicy.io/](https://editor.networkpolicy.io/)  
28. API | OpenCost — open source cost monitoring for cloud native environments, accessed January 19, 2026, [https://opencost.io/docs/integrations/api/](https://opencost.io/docs/integrations/api/)  
29. API Examples | OpenCost — open source cost monitoring for cloud native environments, accessed January 19, 2026, [https://opencost.io/docs/integrations/api-examples/](https://opencost.io/docs/integrations/api-examples/)  
30. Product Hunt launch: 6 lessons from founders who survived (and won) \- Waveup, accessed January 19, 2026, [https://waveup.com/blog/product-hunt-launch-6-lessons-from-founders-who-survived-and-won/](https://waveup.com/blog/product-hunt-launch-6-lessons-from-founders-who-survived-and-won/)  
31. Product Hunt Launch Playbook: How To Become \#1 in 2025 \- Arc Employer Blog, accessed January 19, 2026, [https://arc.dev/employer-blog/product-hunt-launch-playbook/](https://arc.dev/employer-blog/product-hunt-launch-playbook/)  
32. Project Metrics | CNCF, accessed January 19, 2026, [https://www.cncf.io/project-metrics/](https://www.cncf.io/project-metrics/)  
33. Sandbox Projects | CNCF, accessed January 19, 2026, [https://www.cncf.io/sandbox-projects/](https://www.cncf.io/sandbox-projects/)  
34. Monetizing Open Source Software | OpenTAP Blog, accessed January 19, 2026, [https://blog.opentap.io/monetizing-open-source-software](https://blog.opentap.io/monetizing-open-source-software)  
35. How companies make millions on Open Source | Tech blog \- Palark, accessed January 19, 2026, [https://palark.com/blog/open-source-business-models/](https://palark.com/blog/open-source-business-models/)  
36. Kubernetes RBAC: A Step-by-Step Guide for Securing Your Cluster \- Trilio, accessed January 19, 2026, [https://trilio.io/kubernetes-best-practices/kubernetes-rbac/](https://trilio.io/kubernetes-best-practices/kubernetes-rbac/)  
37. Step-by-Step Guide to Hosting Your Own Helm Chart Registry on GitHub Pages \- Medium, accessed January 19, 2026, [https://medium.com/@blackhorseya/step-by-step-guide-to-hosting-your-own-helm-chart-registry-on-github-pages-c37809a1d93f](https://medium.com/@blackhorseya/step-by-step-guide-to-hosting-your-own-helm-chart-registry-on-github-pages-c37809a1d93f)