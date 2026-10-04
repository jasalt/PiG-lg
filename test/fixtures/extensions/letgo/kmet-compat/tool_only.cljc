(ns kmet-compat.tool-only
  (:require #?(:lg [pig.extension :as ext]
               :default [kmet.extension :as ext])))

(def log (atom []))

(defn init [api]
  (ext/register-tool!
   api
   {:name "echo"
    :description "Echo the text argument"
    :parameters {:type "object"
                 :properties {:text {:type "string"}}
                 :required ["text"]}
    :execute (fn [args]
               (swap! log conj (str "called " (:text args)))
               {:content (str "echo: " (:text args))})}))
