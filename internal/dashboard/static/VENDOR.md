# Dashboard static vendors

## Cytoscape.js (primary graph editor)

- File: `vendor/cytoscape.min.js`
- Version: **3.30.4**
- License: MIT
- Source: https://js.cytoscape.org/ (npm package `cytoscape`)
- Used by: Graph editor tab -- hoop stage pipeline + swarm thread graphs (nodes, edges, pan/zoom, drag)
- Why this library: mature vanilla JS graph editor (no React), MIT, editable nodes/edges, large-canvas pan/zoom, small enough to vendor; better fit than Drawflow/LiteGraph for freeform loop+swarm graphs, and lighter than JointJS / Rete / xyflow for this dashboard.

## cytoscape-edgehandles

- File: `vendor/cytoscape-edgehandles.min.js`
- Version: **4.0.1**
- License: MIT
- Source: npm `cytoscape-edgehandles`
- Used by: Graph editor **Link mode** -- drag node-to-node to draw / reconnect edges on stage and swarm canvases (v4 uses draw mode; toggle via the Link mode toolbar button)
- Depends on: `vendor/lodash-shim.js` loaded before this script

## lodash shim (edgehandles UMD deps only)

- File: `vendor/lodash-shim.js`
- Provides `_.memoize` and `_.throttle` expected by the edgehandles browser UMD build (avoids pulling full lodash).

### Update

Replace files under `vendor/` from the matching npm `dist` builds, then bump versions here and in the HTML comments.

CDN fallback (optional, not used by default):

```html
<script src="https://cdn.jsdelivr.net/npm/cytoscape@3.30.4/dist/cytoscape.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/cytoscape-edgehandles@4.0.1/cytoscape-edgehandles.min.js"></script>
```
