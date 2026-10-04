(ns pig.context)

;; Thin wrappers over the callback context map. A callback receives ctx as a
;; plain map: :cwd, :mode, :has-ui and :model are snapshots taken when the
;; callback starts; every other key holds a host-backed function.
;; signal-cancelled? is installed natively because it takes a signal.

(defn cwd [ctx] (:cwd ctx))

(defn mode [ctx] (:mode ctx))

(defn model [ctx] (:model ctx))

(defn has-ui? [ctx] (:has-ui ctx))

(defn is-idle? [ctx] ((:is-idle ctx)))

(defn request-cancelled? [ctx] ((:request-cancelled ctx)))

(defn signal [ctx] ((:signal ctx)))

(defn get-active-tools [ctx] ((:get-active-tools ctx)))

(defn get-all-tools [ctx] ((:get-all-tools ctx)))

(defn get-entries [ctx] ((:get-entries ctx)))

(defn get-branch [ctx] ((:get-branch ctx)))

(defn notify!
  ([ctx message] ((:notify ctx) message))
  ([ctx message level] ((:notify ctx) message level)))

(defn select! [ctx title options] ((:select ctx) title options))

(defn confirm! [ctx title message] ((:confirm ctx) title message))

(defn input!
  ([ctx title] ((:input ctx) title))
  ([ctx title placeholder] ((:input ctx) title placeholder)))
