/**
 * The name of the tree's root — the row above the hypervisor, and the first
 * step of every breadcrumb.
 *
 * One constant rather than a literal at each of its seven call sites, because
 * the sidebar and the breadcrumbs have to agree: a trail reading
 * "Datacenter › MiMax2" under a sidebar row labelled "Station" is the kind of
 * drift nobody notices until it is in a screenshot.
 *
 * Proxmox calls it Datacenter, and the code still does — the endpoint is
 * /api/datacenter and the view is DatacenterView. This is only what it is
 * called on screen: a station, with Outposts reporting to it.
 */
export const ROOT_LABEL = "Station"
