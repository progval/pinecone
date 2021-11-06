import { Graph } from "./modules/graph.js";
import "./modules/ui.js";

const worker = new Worker("ui/websocket-worker.js");
export var graph = new Graph(document.getElementById("canvas"));

function handleSimMessage(msg) {
    // console.log(msg.data);
    switch(msg.data.MsgID) {
    case 1: // Initial State
        for (let i = 0; i < msg.data.Nodes.length; i++) {
            graph.addNode(msg.data.Nodes[i]);
        }

        for (let [key, value] of Object.entries(msg.data.PhysEdges)) {
            for (let i = 0; i < msg.data.PhysEdges[key].length; i++) {
                graph.addEdge("physical", key, msg.data.PhysEdges[key][i]);
            }
        }

        for (let [key, value] of Object.entries(msg.data.SnakeEdges)) {
            for (let i = 0; i < msg.data.SnakeEdges[key].length; i++) {
                graph.addEdge("snake", key, msg.data.SnakeEdges[key][i]);
            }
        }

        for (let [key, value] of Object.entries(msg.data.TreeEdges)) {
            for (let i = 0; i < msg.data.TreeEdges[key].length; i++) {
                graph.addEdge("tree", key, msg.data.TreeEdges[key][i]);
            }
        }

        if (msg.data.End === true) {
            graph.startGraph();
        }
        break;
    case 2: // State Update
        for (let i = 0; i < msg.data.Events.length; i++) {
            let event = msg.data.Events[i].Event;
            switch(msg.data.Events[i].UpdateID) {
            case 1: // Node Added
                // console.log("Node added " + event.Node);
                graph.addNode(event.Node);
                break;
            case 2: // Node Removed
                // console.log("Node removed " + event.Node);
                graph.removeNode(event.Node);
                break;
            case 3: // Peer Added
                // console.log("Peer added: Node: " + event.Node + " Peer: " + event.Peer);
                graph.addEdge("physical", event.Node, event.Peer);
                break;
            case 4: // Peer Removed
                // console.log("Peer removed: Node: " + event.Node + " Peer: " + event.Peer);
                graph.removeEdge("physical", event.Node, event.Peer);
                break;
            case 5: // Snake Ascending Updated
                // console.log("Snake Asc Updated: Node: " + event.Node + " Peer: " + event.Peer);
                graph.removeEdge("snake", event.Node, event.Prev);
                if (event.Peer != "") {
                    graph.addEdge("snake", event.Node, event.Peer);
                }
                break;
            case 6: // Snake Descending Updated
                // console.log("Snake Desc Updated: Node: " + event.Node + " Peer: " + event.Peer);
                graph.removeEdge("snake", event.Node, event.Prev);
                if (event.Peer != "") {
                    graph.addEdge("snake", event.Node, event.Peer);
                }
                break;
            }
        }
        break;
    default:
        console.log("Unhandled message ID");
        break;
    }
};

worker.onmessage = handleSimMessage;

// Start the websocket worker with the current url
worker.postMessage({url: window.origin.replace("http", "ws") + '/ws'});
