(ns kmet-compat.helper-entry
  (:require #?(:lg [pig.extension :as ext]
               :default [kmet.extension :as ext])
            [kmet-compat.text :as text]))

(def log (atom []))

(defn init [api]
  (ext/register-tool!
   api
   {:name "describe"
    :description "Describe a text with a pure helper"
    :parameters {:type "object"
                 :properties {:text {:type "string"}}
                 :required ["text"]}
    :execute (fn [args]
               {:content (text/describe (:text args))})}))
