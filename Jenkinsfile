// The Assembly Line: four stations, one product.
// Jenkins (Clever Cloud add-on) runs this on a Clever Cloud runner, a Docker
// app started for the build and deleted after it. The product is this repo.
pipeline {
  agent { label 'clever-go' }

  options {
    skipDefaultCheckout()          // checkout is a station of its own
    disableConcurrentBuilds()
    buildDiscarder(logRotator(numToKeepStr: '30'))
  }

  parameters {
    booleanParam(name: 'SHIP', defaultValue: true,
      description: 'Deploy the product to Clever Cloud once the tests pass')
  }

  environment {
    GO_VERSION     = 'go1.27.2'
    CLEVER_VERSION = '5.2.0'
    // PRODUCT_APP_ID, the Clever Cloud app this line deploys (app_...), is not
    // written here: set it in Manage Jenkins › System › Global properties ›
    // Environment variables. The Deploy stage stops with an error without it.
  }

  stages {
    stage('Checkout') {
      steps {
        checkout scm
        sh 'git log -1 --format="Product commit %h: %s"'
      }
    }

    stage('Test') {
      steps {
        sh 'ci/go vet ./...'
        sh 'ci/go test ./...'
      }
    }

    stage('Build') {
      steps {
        sh 'CGO_ENABLED=0 ci/go build -trimpath -o dist/product .'
        sh 'ls -lh dist/product'
      }
    }

    stage('Deploy') {
      when { expression { params.SHIP != false } }
      environment {
        // Jenkins "Secret text" credentials: masked in the console, never echoed.
        CLEVER_TOKEN  = credentials('clever-token')
        CLEVER_SECRET = credentials('clever-secret')
      }
      steps {
        sh '''
          if [ -z "${PRODUCT_APP_ID:-}" ]; then
            echo "PRODUCT_APP_ID is not set. Set it to the product app id (app_...) in Manage Jenkins > System > Global properties > Environment variables." >&2
            exit 1
          fi
        '''
        sh 'rm -f .clever.json && ci/clever link "$PRODUCT_APP_ID" --alias product'
        sh 'ci/clever deploy --alias product --force --same-commit-policy rebuild'
      }
    }
  }
}
